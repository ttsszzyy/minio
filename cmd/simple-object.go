// Copyright (c) 2015-2024 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"encoding/base64"
	"hash/crc32"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/minio/minio-go/v7/pkg/tags"
	xhttp "github.com/minio/minio/internal/http"
)

// simpleObjects - 简化的对象存储,无擦除码
// 支持多个存储端点实现负载均衡
type simpleObjects struct {
	storages []StorageAPI // 多个存储端点
	nextIdx  uint32       // 用于轮询负载均衡的计数器
}

// NewSimpleObjects - 创建简化对象层(单个存储)
func NewSimpleObjects(storage StorageAPI) ObjectLayer {
	return &simpleObjects{
		storages: []StorageAPI{storage},
		nextIdx:  0,
	}
}

// NewSimpleObjectsMulti - 创建简化对象层(多个存储,负载均衡)
func NewSimpleObjectsMulti(storages []StorageAPI) ObjectLayer {
	return &simpleObjects{
		storages: storages,
		nextIdx:  0,
	}
}

// selectStorage - 使用一致性哈希选择存储端点
// 根据 bucket 和 object 计算哈希,确保同一对象总是路由到同一存储
func (s *simpleObjects) selectStorage(bucket, object string) StorageAPI {
	if len(s.storages) == 1 {
		return s.storages[0]
	}

	// 使用 crc32 计算哈希值,保证同一对象总是路由到同一存储
	h := crc32.NewIEEE()
	h.Write([]byte(bucket + "/" + object))
	idx := h.Sum32() % uint32(len(s.storages))
	return s.storages[idx]
}

// selectStorageBucket - 为 bucket 操作选择存储
// 使用第一个存储处理 bucket 操作,保证一致性
func (s *simpleObjects) selectStorageBucket(bucket string) StorageAPI {
	if len(s.storages) == 1 {
		return s.storages[0]
	}
	// bucket 操作使用第一个存储,确保 bucket 列表一致性
	return s.storages[0]
}

// Shutdown - 关闭存储
func (s *simpleObjects) Shutdown(ctx context.Context) error {
	return nil
}

// NSScanner - 命名空间扫描(简化版不支持)
func (s *simpleObjects) NSScanner(ctx context.Context, updates chan<- DataUsageInfo, wantCycle uint32, scanMode madmin.HealScanMode) error {
	// 简化版本,不做命名空间扫描
	return nil
}

// BackendInfo - 返回后端信息
func (s *simpleObjects) BackendInfo() madmin.BackendInfo {
	return madmin.BackendInfo{
		Type: madmin.FS,
	}
}

// StorageInfo - 存储信息
func (s *simpleObjects) StorageInfo(ctx context.Context, metrics bool) StorageInfo {
	// 汇总所有存储的信息
	var totalSpace, usedSpace, freeSpace uint64
	disks := make([]madmin.Disk, 0, len(s.storages))

	for _, storage := range s.storages {
		info, _ := storage.DiskInfo(ctx, DiskInfoOptions{})
		totalSpace += info.Total
		usedSpace += info.Used
		freeSpace += info.Free

		disks = append(disks, madmin.Disk{
			State:          madmin.DriveStateOk,
			TotalSpace:     info.Total,
			UsedSpace:      info.Used,
			AvailableSpace: info.Free,
		})
	}

	return StorageInfo{
		Disks: disks,
	}
}

// LocalStorageInfo - 本地存储信息
func (s *simpleObjects) LocalStorageInfo(ctx context.Context, metrics bool) StorageInfo {
	return s.StorageInfo(ctx, metrics)
}

// NewNSLock - 创建命名空间锁
func (s *simpleObjects) NewNSLock(bucket string, objects ...string) RWLocker {
	return &noopRWLocker{}
}

// Legacy - 是否是遗留部署
func (s *simpleObjects) Legacy() bool {
	return false
}

// SetDriveCounts - 设置驱动器数量
func (s *simpleObjects) SetDriveCounts() []int {
	return []int{1}
}

// GetDisks - 获取磁盘
func (s *simpleObjects) GetDisks(poolIdx, setIdx int) ([]StorageAPI, error) {
	return s.storages, nil
}

// noopRWLocker - 空锁实现,实现 RWLocker 接口
type noopRWLocker struct{}

func (n *noopRWLocker) GetLock(ctx context.Context, timeout *dynamicTimeout) (LockContext, error) {
	return LockContext{ctx: ctx, cancel: nil}, nil
}

func (n *noopRWLocker) Unlock(lkCtx LockContext) {}

func (n *noopRWLocker) GetRLock(ctx context.Context, timeout *dynamicTimeout) (LockContext, error) {
	return LockContext{ctx: ctx, cancel: nil}, nil
}

func (n *noopRWLocker) RUnlock(lkCtx LockContext) {}

// PutObject - 上传对象(无擦除码)
func (s *simpleObjects) PutObject(ctx context.Context, bucket, object string, data *PutObjReader, opts ObjectOptions) (ObjectInfo, error) {
	// 选择存储端点 - 使用一致性哈希
	storage := s.selectStorage(bucket, object)

	// 构建对象路径
	objectPath := pathJoin(bucket, object)
	dataPath := pathJoin(objectPath, "data")
	metaPath := pathJoin(objectPath, "xl.meta")

	// 从opts中提取xl.meta
	xlMetaEncoded := opts.UserDefined["X-Amz-Meta-Xl-Meta"]
	var xlMetaBytes []byte

	if xlMetaEncoded != "" {
		var err error
		xlMetaBytes, err = base64.StdEncoding.DecodeString(xlMetaEncoded)
		if err != nil {
			return ObjectInfo{}, err
		}
		delete(opts.UserDefined, "X-Amz-Meta-Xl-Meta")
	}

	// 读取完整对象数据到内存
	dataBytes, err := io.ReadAll(io.LimitReader(data.Reader, data.Size()))
	if err != nil {
		return ObjectInfo{}, err
	}

	// 写入对象数据 - WriteAll 签名: (ctx, volume, path string, b []byte) error
	err = storage.WriteAll(ctx, bucket, dataPath, dataBytes)
	if err != nil {
		return ObjectInfo{}, err
	}

	actualSize := int64(len(dataBytes))

	// 保存xl.meta
	if len(xlMetaBytes) > 0 {
		err = storage.WriteAll(ctx, bucket, metaPath, xlMetaBytes)
		if err != nil {
			// 清理已写入的数据
			storage.Delete(ctx, bucket, dataPath, DeleteOptions{})
			return ObjectInfo{}, err
		}
	} else {
		// 生成基本的xl.meta
		fi := FileInfo{
			Name:      object,
			Size:      actualSize,
			ModTime:   time.Now(),
			VersionID: opts.VersionID,
			Metadata:  opts.UserDefined,
		}
		xlMetaBytes, _ = fi.MarshalMsg(nil)
		storage.WriteAll(ctx, bucket, metaPath, xlMetaBytes)
	}

	return ObjectInfo{
		Bucket:      bucket,
		Name:        object,
		Size:        actualSize,
		ModTime:     time.Now(),
		UserDefined: opts.UserDefined,
		VersionID:   opts.VersionID,
	}, nil
}

// GetObjectNInfo - 获取对象(无解码)
func (s *simpleObjects) GetObjectNInfo(ctx context.Context, bucket, object string, rs *HTTPRangeSpec, h http.Header, opts ObjectOptions) (*GetObjectReader, error) {
	// 选择存储端点
	storage := s.selectStorage(bucket, object)

	// 获取对象信息
	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return nil, err
	}

	// 构建数据路径
	objectPath := pathJoin(bucket, object)
	dataPath := pathJoin(objectPath, "data")
	metaPath := pathJoin(objectPath, "xl.meta")

	// 读取xl.meta - ReadAll 签名: (ctx, volume, path string) ([]byte, error)
	xlMetaData, _ := storage.ReadAll(ctx, bucket, metaPath)
	if len(xlMetaData) > 0 {
		if objInfo.UserDefined == nil {
			objInfo.UserDefined = make(map[string]string)
		}
		objInfo.UserDefined["X-Amz-Meta-Xl-Meta"] = base64.StdEncoding.EncodeToString(xlMetaData)
	}

	// 处理Range请求
	offset := int64(0)
	length := objInfo.Size
	if rs != nil {
		offset = rs.Start
		if rs.End > 0 {
			length = rs.End - rs.Start + 1
		}
	}

	// 打开数据文件 - ReadFileStream 签名: (ctx, volume, path string, offset, length int64) (io.ReadCloser, error)
	reader, err := storage.ReadFileStream(ctx, bucket, dataPath, offset, length)
	if err != nil {
		return nil, err
	}

	gr := &GetObjectReader{
		ObjInfo: objInfo,
		Reader:  reader,
	}

	return gr, nil
}

// GetObjectInfo - 获取对象信息
func (s *simpleObjects) GetObjectInfo(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	// 选择存储端点
	storage := s.selectStorage(bucket, object)

	objectPath := pathJoin(bucket, object)
	metaPath := pathJoin(objectPath, "xl.meta")

	// 读取xl.meta - ReadAll 签名: (ctx, volume, path string) ([]byte, error)
	xlMetaBytes, err := storage.ReadAll(ctx, bucket, metaPath)
	if err != nil {
		return ObjectInfo{}, toObjectErr(err, bucket, object)
	}

	// 解析元数据
	var fi FileInfo
	if _, err := fi.UnmarshalMsg(xlMetaBytes); err != nil {
		return ObjectInfo{}, err
	}

	return ObjectInfo{
		Bucket:      bucket,
		Name:        object,
		Size:        fi.Size,
		ModTime:     fi.ModTime,
		UserDefined: fi.Metadata,
		VersionID:   fi.VersionID,
	}, nil
}

// CopyObject - 复制对象
func (s *simpleObjects) CopyObject(ctx context.Context, srcBucket, srcObject, dstBucket, dstObject string, srcInfo ObjectInfo, srcOpts, dstOpts ObjectOptions) (ObjectInfo, error) {
	// 源和目标可能在不同的存储端点
	srcStorage := s.selectStorage(srcBucket, srcObject)
	dstStorage := s.selectStorage(dstBucket, dstObject)

	// 读取源对象数据 - ReadAll 签名: (ctx, volume, path string) ([]byte, error)
	srcPath := pathJoin(srcBucket, srcObject, "data")
	srcData, err := srcStorage.ReadAll(ctx, srcBucket, srcPath)
	if err != nil {
		return ObjectInfo{}, err
	}

	// 读取源xl.meta
	srcMetaPath := pathJoin(srcBucket, srcObject, "xl.meta")
	srcMeta, _ := srcStorage.ReadAll(ctx, srcBucket, srcMetaPath)

	// 写入目标对象
	dstPath := pathJoin(dstBucket, dstObject)
	dstDataPath := pathJoin(dstPath, "data")
	dstMetaPath := pathJoin(dstPath, "xl.meta")

	// WriteAll 签名: (ctx, volume, path string, b []byte) error
	err = dstStorage.WriteAll(ctx, dstBucket, dstDataPath, srcData)
	if err != nil {
		return ObjectInfo{}, err
	}

	if len(srcMeta) > 0 {
		dstStorage.WriteAll(ctx, dstBucket, dstMetaPath, srcMeta)
	}

	return s.GetObjectInfo(ctx, dstBucket, dstObject, dstOpts)
}

// DeleteObject - 删除对象
func (s *simpleObjects) DeleteObject(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	// 选择存储端点
	storage := s.selectStorage(bucket, object)

	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}

	objectPath := pathJoin(bucket, object)
	dataPath := pathJoin(objectPath, "data")
	metaPath := pathJoin(objectPath, "xl.meta")

	// 删除数据和元数据 - Delete 签名: (ctx, volume, path string, opts DeleteOptions) error
	storage.Delete(ctx, bucket, dataPath, DeleteOptions{})
	storage.Delete(ctx, bucket, metaPath, DeleteOptions{})
	storage.Delete(ctx, bucket, objectPath, DeleteOptions{})

	return objInfo, nil
}

// DeleteObjects - 批量删除对象
func (s *simpleObjects) DeleteObjects(ctx context.Context, bucket string, objects []ObjectToDelete, opts ObjectOptions) ([]DeletedObject, []error) {
	deleted := make([]DeletedObject, len(objects))
	errs := make([]error, len(objects))

	for i, obj := range objects {
		_, errs[i] = s.DeleteObject(ctx, bucket, obj.ObjectName, opts)
		if errs[i] == nil {
			deleted[i] = DeletedObject{
				ObjectName: obj.ObjectName,
				VersionID:  obj.VersionID,
			}
		}
	}

	return deleted, errs
}

// ListObjects - 列出对象
func (s *simpleObjects) ListObjects(ctx context.Context, bucket, prefix, marker, delimiter string, maxKeys int) (ListObjectsInfo, error) {
	storage := s.selectStorageBucket(bucket)

	// 使用ListDir列出目录
	entries, err := storage.ListDir(ctx, "", bucket, prefix, -1)
	if err != nil {
		return ListObjectsInfo{}, err
	}

	var objects []ObjectInfo
	var prefixes []string
	prefixMap := make(map[string]bool)

	for _, entry := range entries {
		// 跳过marker之前的对象
		if marker != "" && entry <= marker {
			continue
		}

		// 检查是否是目录分隔符
		if delimiter != "" && strings.HasSuffix(entry, delimiter) {
			if !prefixMap[entry] {
				prefixMap[entry] = true
				prefixes = append(prefixes, entry)
			}
			continue
		}

		// 获取对象信息
		objInfo, err := s.GetObjectInfo(ctx, bucket, entry, ObjectOptions{})
		if err == nil {
			objects = append(objects, objInfo)
			if len(objects) >= maxKeys {
				break
			}
		}
	}

	isTruncated := len(objects) == maxKeys
	nextMarker := ""
	if isTruncated && len(objects) > 0 {
		nextMarker = objects[len(objects)-1].Name
	}

	return ListObjectsInfo{
		IsTruncated: isTruncated,
		NextMarker:  nextMarker,
		Objects:     objects,
		Prefixes:    prefixes,
	}, nil
}

// ListObjectsV2 - 列出对象V2
func (s *simpleObjects) ListObjectsV2(ctx context.Context, bucket, prefix, continuationToken, delimiter string, maxKeys int, fetchOwner bool, startAfter string) (ListObjectsV2Info, error) {
	marker := continuationToken
	if marker == "" {
		marker = startAfter
	}

	v1Result, err := s.ListObjects(ctx, bucket, prefix, marker, delimiter, maxKeys)
	if err != nil {
		return ListObjectsV2Info{}, err
	}

	return ListObjectsV2Info{
		IsTruncated:           v1Result.IsTruncated,
		ContinuationToken:     continuationToken,
		NextContinuationToken: v1Result.NextMarker,
		Objects:               v1Result.Objects,
		Prefixes:              v1Result.Prefixes,
	}, nil
}

// ListObjectVersions - 列出对象版本
func (s *simpleObjects) ListObjectVersions(ctx context.Context, bucket, prefix, marker, versionMarker, delimiter string, maxKeys int) (ListObjectVersionsInfo, error) {
	// 简化版不支持版本控制,返回当前版本即可
	v1Result, err := s.ListObjects(ctx, bucket, prefix, marker, delimiter, maxKeys)
	if err != nil {
		return ListObjectVersionsInfo{}, err
	}

	return ListObjectVersionsInfo{
		IsTruncated: v1Result.IsTruncated,
		Objects:     v1Result.Objects,
		Prefixes:    v1Result.Prefixes,
	}, nil
}

// Walk - 遍历对象
func (s *simpleObjects) Walk(ctx context.Context, bucket, prefix string, results chan<- itemOrErr[ObjectInfo], opts WalkOptions) error {
	storage := s.selectStorageBucket(bucket)

	// 使用ListDir递归列出所有对象
	entries, err := storage.ListDir(ctx, "", bucket, prefix, -1)
	if err != nil {
		close(results)
		return err
	}

	go func() {
		defer close(results)
		for _, entry := range entries {
			if !strings.HasSuffix(entry, SlashSeparator) {
				objInfo, err := s.GetObjectInfo(ctx, bucket, entry, ObjectOptions{})
				if err != nil {
					results <- itemOrErr[ObjectInfo]{Err: err}
					continue
				}
				results <- itemOrErr[ObjectInfo]{Item: objInfo}
			}
		}
	}()

	return nil
}

// TransitionObject - 转换对象(简化版不支持对象转换)
func (s *simpleObjects) TransitionObject(ctx context.Context, bucket, object string, opts ObjectOptions) error {
	// 简化版本不支持对象转换到其他存储层
	return nil
}

// RestoreTransitionedObject - 恢复转换的对象(简化版不支持)
func (s *simpleObjects) RestoreTransitionedObject(ctx context.Context, bucket, object string, opts ObjectOptions) error {
	// 简化版本不支持恢复转换的对象
	return nil
}

// Health - 健康检查
func (s *simpleObjects) Health(ctx context.Context, opts HealthOptions) HealthResult {
	return HealthResult{Healthy: true, HealthyRead: true}
}

// CheckAbandonedParts - 检查废弃的分片(简化版不支持)
func (s *simpleObjects) CheckAbandonedParts(ctx context.Context, bucket, object string, opts madmin.HealOpts) error {
	// 简化版本,不做废弃分片检查
	return nil
}

// DecomTieredObject - 解压分层对象(简化版不支持)
func (s *simpleObjects) DecomTieredObject(ctx context.Context, bucket, object string, fi FileInfo, opts ObjectOptions) error {
	// 简化版本不支持分层对象
	return nil
}

// CopyObjectPart - 复制对象分片
func (s *simpleObjects) CopyObjectPart(ctx context.Context, srcBucket, srcObject, destBucket, destObject string, uploadID string, partID int, startOffset int64, length int64, srcInfo ObjectInfo, srcOpts, dstOpts ObjectOptions) (PartInfo, error) {
	srcStorage := s.selectStorage(srcBucket, srcObject)
	dstStorage := s.selectStorage(destBucket, destObject)

	// 读取源对象数据
	srcPath := pathJoin(srcBucket, srcObject, "data")
	srcData, err := srcStorage.ReadAll(ctx, srcBucket, srcPath)
	if err != nil {
		return PartInfo{}, err
	}

	// 提取指定范围的数据
	if startOffset < 0 || startOffset >= int64(len(srcData)) {
		startOffset = 0
	}
	endOffset := startOffset + length
	if endOffset > int64(len(srcData)) {
		endOffset = int64(len(srcData))
	}
	partData := srcData[startOffset:endOffset]

	// 保存为分片
	partPath := pathJoin(uploadID, "part."+strconv.Itoa(partID))
	err = dstStorage.WriteAll(ctx, minioMetaTmpBucket, partPath, partData)
	if err != nil {
		return PartInfo{}, err
	}

	return PartInfo{
		PartNumber: partID,
		Size:       int64(len(partData)),
		ETag:       getMD5Hash(partData),
	}, nil
}

// PutObjectMetadata - 更新对象元数据
func (s *simpleObjects) PutObjectMetadata(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	// 选择存储端点
	storage := s.selectStorage(bucket, object)

	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}

	// 更新元数据
	metaPath := pathJoin(bucket, object, "xl.meta")
	fi := FileInfo{
		Name:      object,
		Size:      objInfo.Size,
		ModTime:   objInfo.ModTime,
		VersionID: objInfo.VersionID,
		Metadata:  opts.UserDefined,
	}

	xlMetaBytes, err := fi.MarshalMsg(nil)
	if err != nil {
		return ObjectInfo{}, err
	}

	err = storage.WriteAll(ctx, bucket, metaPath, xlMetaBytes)
	if err != nil {
		return ObjectInfo{}, err
	}

	return s.GetObjectInfo(ctx, bucket, object, opts)
}

// PutObjectTags - 设置对象标签
func (s *simpleObjects) PutObjectTags(ctx context.Context, bucket, object string, tags string, opts ObjectOptions) (ObjectInfo, error) {
	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}

	if objInfo.UserDefined == nil {
		objInfo.UserDefined = make(map[string]string)
	}
	objInfo.UserDefined[xhttp.AmzObjectTagging] = tags

	return s.PutObjectMetadata(ctx, bucket, object, ObjectOptions{UserDefined: objInfo.UserDefined})
}

// GetObjectTags - 获取对象标签
func (s *simpleObjects) GetObjectTags(ctx context.Context, bucket, object string, opts ObjectOptions) (*tags.Tags, error) {
	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return nil, err
	}

	tagsStr := objInfo.UserDefined[xhttp.AmzObjectTagging]
	if tagsStr == "" {
		return nil, nil
	}

	return tags.ParseObjectTags(tagsStr)
}

// DeleteObjectTags - 删除对象标签
func (s *simpleObjects) DeleteObjectTags(ctx context.Context, bucket, object string, opts ObjectOptions) (ObjectInfo, error) {
	objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
	if err != nil {
		return ObjectInfo{}, err
	}

	if objInfo.UserDefined != nil {
		delete(objInfo.UserDefined, xhttp.AmzObjectTagging)
	}

	return s.PutObjectMetadata(ctx, bucket, object, ObjectOptions{UserDefined: objInfo.UserDefined})
}

// MakeBucket - 创建存储桶
func (s *simpleObjects) MakeBucket(ctx context.Context, bucket string, opts MakeBucketOptions) error {
	// bucket操作使用第一个存储
	storage := s.selectStorageBucket(bucket)
	// 使用 MakeVol 创建卷(bucket)
	return storage.MakeVol(ctx, bucket)
}

// GetBucketInfo - 获取存储桶信息
func (s *simpleObjects) GetBucketInfo(ctx context.Context, bucket string, opts BucketOptions) (BucketInfo, error) {
	// bucket操作使用第一个存储
	storage := s.selectStorageBucket(bucket)
	// 检查bucket是否存在
	_, err := storage.StatVol(ctx, bucket)
	if err != nil {
		return BucketInfo{}, err
	}

	return BucketInfo{
		Name:    bucket,
		Created: time.Now(),
	}, nil
}

// ListBuckets - 列出所有存储桶
func (s *simpleObjects) ListBuckets(ctx context.Context, opts BucketOptions) ([]BucketInfo, error) {
	// bucket操作使用第一个存储
	storage := s.selectStorageBucket("")
	vols, err := storage.ListVols(ctx)
	if err != nil {
		return nil, err
	}

	buckets := make([]BucketInfo, 0, len(vols))
	for _, vol := range vols {
		buckets = append(buckets, BucketInfo{
			Name:    vol.Name,
			Created: vol.Created,
		})
	}

	return buckets, nil
}

// DeleteBucket - 删除存储桶
func (s *simpleObjects) DeleteBucket(ctx context.Context, bucket string, opts DeleteBucketOptions) error {
	// bucket操作使用第一个存储
	storage := s.selectStorageBucket(bucket)
	return storage.DeleteVol(ctx, bucket, false)
}

// IsNotificationSupported - 是否支持通知
func (s *simpleObjects) IsNotificationSupported() bool {
	return false
}

// IsListenSupported - 是否支持监听
func (s *simpleObjects) IsListenSupported() bool {
	return false
}

// IsEncryptionSupported - 是否支持加密
func (s *simpleObjects) IsEncryptionSupported() bool {
	return false
}

// IsCompressionSupported - 是否支持压缩
func (s *simpleObjects) IsCompressionSupported() bool {
	return false
}

// IsTaggingSupported - 是否支持标签
func (s *simpleObjects) IsTaggingSupported() bool {
	return true
}

// CheckQuorum - 检查法定人数
func (s *simpleObjects) CheckQuorum(ctx context.Context, bucket, prefix string, readQuorum int) error {
	return nil
}

// HealFormat - 修复格式(简化版不需要修复)
func (s *simpleObjects) HealFormat(ctx context.Context, dryRun bool) (madmin.HealResultItem, error) {
	// 简化版本无擦除码,不需要修复
	return madmin.HealResultItem{}, nil
}

// HealBucket - 修复存储桶(简化版不需要修复)
func (s *simpleObjects) HealBucket(ctx context.Context, bucket string, opts madmin.HealOpts) (madmin.HealResultItem, error) {
	// 简化版本无擦除码,不需要修复bucket
	return madmin.HealResultItem{}, nil
}

// HealObject - 修复对象(简化版不需要修复)
func (s *simpleObjects) HealObject(ctx context.Context, bucket, object, versionID string, opts madmin.HealOpts) (madmin.HealResultItem, error) {
	// 简化版本无擦除码,不需要修复object
	return madmin.HealResultItem{}, nil
}

// HealObjects - 修复多个对象(简化版不需要修复)
func (s *simpleObjects) HealObjects(ctx context.Context, bucket, prefix string, opts madmin.HealOpts, fn HealObjectFn) error {
	// 简化版本无擦除码,不需要修复objects
	return nil
}

// GetMultipartInfo - 获取分片上传信息
func (s *simpleObjects) GetMultipartInfo(ctx context.Context, bucket, object, uploadID string, opts ObjectOptions) (MultipartInfo, error) {
	storage := s.selectStorage(bucket, object)

	// 读取上传元数据
	metaPath := pathJoin(uploadID, "upload.meta")
	metaBytes, err := storage.ReadAll(ctx, minioMetaTmpBucket, metaPath)
	if err != nil {
		return MultipartInfo{}, err
	}

	var fi FileInfo
	if _, err := fi.UnmarshalMsg(metaBytes); err != nil {
		return MultipartInfo{}, err
	}

	return MultipartInfo{
		Bucket:    bucket,
		Object:    object,
		UploadID:  uploadID,
		Initiated: fi.ModTime,
	}, nil
}

// NewMultipartUpload - 创建分片上传
func (s *simpleObjects) NewMultipartUpload(ctx context.Context, bucket, object string, opts ObjectOptions) (*NewMultipartUploadResult, error) {
	storage := s.selectStorage(bucket, object)

	// 生成uploadID
	uploadID := mustGetUUID()

	// 创建临时目录存储分片
	uploadPath := pathJoin(bucket, minioMetaTmpBucket, uploadID)
	err := storage.MakeVol(ctx, uploadPath)
	if err != nil {
		return nil, err
	}

	// 保存元数据
	fi := FileInfo{
		Name:      object,
		ModTime:   time.Now(),
		Metadata:  opts.UserDefined,
		VersionID: opts.VersionID,
	}

	metaBytes, _ := fi.MarshalMsg(nil)
	metaPath := pathJoin(uploadPath, "upload.meta")
	storage.WriteAll(ctx, minioMetaTmpBucket, metaPath, metaBytes)

	return &NewMultipartUploadResult{
		UploadID: uploadID,
	}, nil
}

// PutObjectPart - 上传分片
func (s *simpleObjects) PutObjectPart(ctx context.Context, bucket, object, uploadID string, partID int, data *PutObjReader, opts ObjectOptions) (PartInfo, error) {
	storage := s.selectStorage(bucket, object)

	// 读取分片数据
	partData, err := io.ReadAll(io.LimitReader(data.Reader, data.Size()))
	if err != nil {
		return PartInfo{}, err
	}

	// 保存分片
	partPath := pathJoin(uploadID, "part."+strconv.Itoa(partID))
	err = storage.WriteAll(ctx, minioMetaTmpBucket, partPath, partData)
	if err != nil {
		return PartInfo{}, err
	}

	return PartInfo{
		PartNumber: partID,
		Size:       int64(len(partData)),
		ETag:       getMD5Hash(partData),
	}, nil
}

// ListObjectParts - 列出对象分片
func (s *simpleObjects) ListObjectParts(ctx context.Context, bucket, object, uploadID string, partNumberMarker int, maxParts int, opts ObjectOptions) (ListPartsInfo, error) {
	storage := s.selectStorage(bucket, object)

	// 列出所有分片
	entries, err := storage.ListDir(ctx, "", minioMetaTmpBucket, uploadID, -1)
	if err != nil {
		return ListPartsInfo{}, err
	}

	var parts []PartInfo
	for _, entry := range entries {
		// 只处理分片文件
		if !strings.HasPrefix(entry, "part.") {
			continue
		}

		// 解析分片号
		partNumStr := strings.TrimPrefix(entry, "part.")
		partNum, err := strconv.Atoi(partNumStr)
		if err != nil || partNum <= partNumberMarker {
			continue
		}

		// 读取分片大小
		partPath := pathJoin(uploadID, entry)
		partData, err := storage.ReadAll(ctx, minioMetaTmpBucket, partPath)
		if err != nil {
			continue
		}

		parts = append(parts, PartInfo{
			PartNumber: partNum,
			Size:       int64(len(partData)),
			ETag:       getMD5Hash(partData),
		})

		if len(parts) >= maxParts {
			break
		}
	}

	return ListPartsInfo{
		Bucket:   bucket,
		Object:   object,
		UploadID: uploadID,
		Parts:    parts,
	}, nil
}

// AbortMultipartUpload - 中止分片上传
func (s *simpleObjects) AbortMultipartUpload(ctx context.Context, bucket, object, uploadID string, opts ObjectOptions) error {
	storage := s.selectStorage(bucket, object)

	// 列出所有分片
	entries, _ := storage.ListDir(ctx, "", minioMetaTmpBucket, uploadID, -1)

	// 删除每个分片
	for _, entry := range entries {
		storage.Delete(ctx, minioMetaTmpBucket, entry, DeleteOptions{})
	}

	// 删除上传目录
	storage.Delete(ctx, minioMetaTmpBucket, uploadID, DeleteOptions{Recursive: true})

	return nil
}

// CompleteMultipartUpload - 完成分片上传
func (s *simpleObjects) CompleteMultipartUpload(ctx context.Context, bucket, object, uploadID string, uploadedParts []CompletePart, opts ObjectOptions) (ObjectInfo, error) {
	storage := s.selectStorage(bucket, object)

	// 读取所有分片并合并
	var allData []byte
	for _, part := range uploadedParts {
		partPath := pathJoin(uploadID, "part."+strconv.Itoa(part.PartNumber))
		partData, err := storage.ReadAll(ctx, minioMetaTmpBucket, partPath)
		if err != nil {
			return ObjectInfo{}, err
		}
		allData = append(allData, partData...)
	}

	// 写入最终对象
	objectPath := pathJoin(bucket, object)
	dataPath := pathJoin(objectPath, "data")
	err := storage.WriteAll(ctx, bucket, dataPath, allData)
	if err != nil {
		return ObjectInfo{}, err
	}

	// 读取并保存元数据
	uploadMetaPath := pathJoin(uploadID, "upload.meta")
	metaBytes, _ := storage.ReadAll(ctx, minioMetaTmpBucket, uploadMetaPath)

	if len(metaBytes) > 0 {
		metaPath := pathJoin(objectPath, "xl.meta")
		storage.WriteAll(ctx, bucket, metaPath, metaBytes)
	}

	// 清理临时文件
	s.AbortMultipartUpload(ctx, bucket, object, uploadID, opts)

	return s.GetObjectInfo(ctx, bucket, object, opts)
}

// ListMultipartUploads - 列出分片上传
func (s *simpleObjects) ListMultipartUploads(ctx context.Context, bucket, prefix, keyMarker, uploadIDMarker, delimiter string, maxUploads int) (ListMultipartsInfo, error) {
	storage := s.selectStorageBucket(bucket)

	// 列出临时目录中的所有上传
	entries, err := storage.ListDir(ctx, "", minioMetaTmpBucket, "", -1)
	if err != nil {
		return ListMultipartsInfo{}, err
	}

	var uploads []MultipartInfo
	for _, uploadID := range entries {
		// 跳过非目录项
		if !strings.HasSuffix(uploadID, SlashSeparator) {
			uploadID = strings.TrimSuffix(uploadID, SlashSeparator)
		}

		// 读取上传元数据
		metaPath := pathJoin(uploadID, "upload.meta")
		metaBytes, err := storage.ReadAll(ctx, minioMetaTmpBucket, metaPath)
		if err != nil {
			continue
		}

		var fi FileInfo
		if _, err := fi.UnmarshalMsg(metaBytes); err != nil {
			continue
		}

		// 过滤不匹配的对象
		if prefix != "" && !strings.HasPrefix(fi.Name, prefix) {
			continue
		}
		if keyMarker != "" && fi.Name <= keyMarker {
			continue
		}

		uploads = append(uploads, MultipartInfo{
			Bucket:    bucket,
			Object:    fi.Name,
			UploadID:  uploadID,
			Initiated: fi.ModTime,
		})

		if len(uploads) >= maxUploads {
			break
		}
	}

	return ListMultipartsInfo{
		Uploads: uploads,
	}, nil
}

// IsCompressionEnabled - 是否启用压缩
func (s *simpleObjects) IsCompressionEnabled() bool {
	return false
}

// IsReady - 是否就绪
func (s *simpleObjects) IsReady(ctx context.Context) bool {
	return true
}

// ReloadFormat - 重新加载格式
func (s *simpleObjects) ReloadFormat(ctx context.Context, dryRun bool) error {
	return nil
}

// ReloadPoolMeta - 重新加载池元数据
func (s *simpleObjects) ReloadPoolMeta(ctx context.Context) error {
	return nil
}
