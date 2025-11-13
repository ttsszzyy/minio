package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/klauspost/reedsolomon"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// FileInfo 结构体 - 与服务器端的 FileInfo 匹配
// 这个结构需要使用 msgp 生成序列化代码
type FileInfo struct {
	Name      string            `msg:"name"`
	Size      int64             `msg:"size"`
	ModTime   time.Time         `msg:"modtime"`
	VersionID string            `msg:"version_id"`
	Metadata  map[string]string `msg:"metadata"`
}

// MarshalMsg 实现 msgp 接口 (简化版,生产环境应使用 msgp 工具生成)
func (fi *FileInfo) MarshalMsg(b []byte) ([]byte, error) {
	// 这里简化实现,实际应该使用 msgp 工具生成
	// 参考: go get github.com/tinylib/msgp
	// 然后运行: msgp -file=fileinfo.go

	// 临时使用 JSON 编码演示(实际应该用 msgp)
	jsonData, err := json.Marshal(fi)
	return jsonData, err
}

// UnmarshalMsg 实现 msgp 接口 (简化版)
func (fi *FileInfo) UnmarshalMsg(b []byte) ([]byte, error) {
	err := json.Unmarshal(b, fi)
	return b, err
}

// ErasureClient - 带擦除码的 MinIO 客户端
type ErasureClient struct {
	client       *minio.Client
	dataShards   int // 数据分片数
	parityShards int // 校验分片数
}

// NewErasureClient 创建擦除码客户端
func NewErasureClient(endpoint, accessKey, secretKey string, dataShards, parityShards int) (*ErasureClient, error) {
	// 创建标准的 MinIO 客户端
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false, // 根据需要设置为 true
	})
	if err != nil {
		return nil, err
	}

	return &ErasureClient{
		client:       minioClient,
		dataShards:   dataShards,
		parityShards: parityShards,
	}, nil
}

// PutObject 上传对象(带擦除编码)
func (ec *ErasureClient) PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, objectSize int64, opts minio.PutObjectOptions) error {
	// 步骤1: 读取完整数据到内存
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read data: %w", err)
	}

	// 步骤2: 擦除编码
	encodedData, err := ec.encodeData(data)
	if err != nil {
		return fmt.Errorf("failed to encode data: %w", err)
	}

	// 步骤3: 生成 xl.meta
	xlMeta := &FileInfo{
		Name:      objectName,
		Size:      int64(len(data)), // 原始大小
		ModTime:   time.Now(),
		VersionID: "",
		Metadata:  opts.UserMetadata,
	}

	// 步骤4: 序列化 xl.meta
	xlMetaBytes, err := xlMeta.MarshalMsg(nil)
	if err != nil {
		return fmt.Errorf("failed to marshal xl.meta: %w", err)
	}

	// 步骤5: Base64 编码 xl.meta
	xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaBytes)

	// 步骤6: 将 xl.meta 放入 Header
	if opts.UserMetadata == nil {
		opts.UserMetadata = make(map[string]string)
	}
	opts.UserMetadata["X-Minio-XL-Meta"] = xlMetaEncoded

	// 步骤7: 上传编码后的数据
	_, err = ec.client.PutObject(ctx, bucketName, objectName,
		bytes.NewReader(encodedData),
		int64(len(encodedData)),
		opts)

	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	log.Printf("Successfully uploaded %s (original: %d bytes, encoded: %d bytes, ratio: %.2f%%)",
		objectName, len(data), len(encodedData), float64(len(encodedData))*100/float64(len(data)))

	return nil
}

// GetObject 下载对象(带擦除解码)
func (ec *ErasureClient) GetObject(ctx context.Context, bucketName, objectName string) ([]byte, *FileInfo, error) {
	// 步骤1: 下载对象
	object, err := ec.client.GetObject(ctx, bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get object: %w", err)
	}
	defer object.Close()

	// 步骤2: 获取对象信息(包含 xl.meta)
	stat, err := object.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stat object: %w", err)
	}

	// 步骤3: 从 Header 中提取 xl.meta
	xlMetaEncoded := stat.Metadata.Get("X-Minio-Xl-Meta")
	if xlMetaEncoded == "" {
		return nil, nil, fmt.Errorf("xl.meta not found in response headers")
	}

	// 步骤4: Base64 解码 xl.meta
	xlMetaBytes, err := base64.StdEncoding.DecodeString(xlMetaEncoded)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode xl.meta: %w", err)
	}

	// 步骤5: 反序列化 xl.meta
	var xlMeta FileInfo
	if _, err := xlMeta.UnmarshalMsg(xlMetaBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal xl.meta: %w", err)
	}

	// 步骤6: 读取编码后的数据
	encodedData, err := io.ReadAll(object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read encoded data: %w", err)
	}

	// 步骤7: 擦除解码
	decodedData, err := ec.decodeData(encodedData, int(xlMeta.Size))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode data: %w", err)
	}

	log.Printf("Successfully downloaded %s (encoded: %d bytes, decoded: %d bytes)",
		objectName, len(encodedData), len(decodedData))

	return decodedData, &xlMeta, nil
}

// encodeData 使用 Reed-Solomon 编码
func (ec *ErasureClient) encodeData(data []byte) ([]byte, error) {
	// 创建 Reed-Solomon 编码器
	enc, err := reedsolomon.New(ec.dataShards, ec.parityShards)
	if err != nil {
		return nil, err
	}

	// 计算每个分片的大小
	totalShards := ec.dataShards + ec.parityShards
	shardSize := (len(data) + ec.dataShards - 1) / ec.dataShards

	// 如果数据不能被均匀分割,需要填充
	paddedSize := shardSize * ec.dataShards
	if len(data) < paddedSize {
		paddedData := make([]byte, paddedSize)
		copy(paddedData, data)
		data = paddedData
	}

	// 分片
	shards := make([][]byte, totalShards)
	for i := 0; i < ec.dataShards; i++ {
		shards[i] = data[i*shardSize : (i+1)*shardSize]
	}

	// 为校验分片分配空间
	for i := ec.dataShards; i < totalShards; i++ {
		shards[i] = make([]byte, shardSize)
	}

	// 编码(生成校验分片)
	if err := enc.Encode(shards); err != nil {
		return nil, err
	}

	// 将所有分片连接起来
	result := make([]byte, 0, shardSize*totalShards)
	for _, shard := range shards {
		result = append(result, shard...)
	}

	return result, nil
}

// decodeData 使用 Reed-Solomon 解码
func (ec *ErasureClient) decodeData(encodedData []byte, originalSize int) ([]byte, error) {
	// 创建 Reed-Solomon 解码器
	enc, err := reedsolomon.New(ec.dataShards, ec.parityShards)
	if err != nil {
		return nil, err
	}

	totalShards := ec.dataShards + ec.parityShards
	shardSize := len(encodedData) / totalShards

	// 分片
	shards := make([][]byte, totalShards)
	for i := 0; i < totalShards; i++ {
		shards[i] = encodedData[i*shardSize : (i+1)*shardSize]
	}

	// 验证和重建(如果有损坏的分片)
	if err := enc.Reconstruct(shards); err != nil {
		return nil, err
	}

	// 合并数据分片
	result := make([]byte, 0, shardSize*ec.dataShards)
	for i := 0; i < ec.dataShards; i++ {
		result = append(result, shards[i]...)
	}

	// 去除填充,返回原始大小的数据
	if len(result) > originalSize {
		result = result[:originalSize]
	}

	return result, nil
}

// 演示主函数
func main() {
	// 配置
	endpoint := "localhost:9000"
	accessKey := "minioadmin"
	secretKey := "minioadmin"
	bucketName := "test-bucket"

	// 创建擦除码客户端
	// 4个数据分片 + 2个校验分片 = 可以容忍2个分片丢失
	client, err := NewErasureClient(endpoint, accessKey, secretKey, 4, 2)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	ctx := context.Background()

	// 确保 bucket 存在
	exists, err := client.client.BucketExists(ctx, bucketName)
	if err != nil {
		log.Fatalf("Failed to check bucket: %v", err)
	}
	if !exists {
		err = client.client.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalf("Failed to create bucket: %v", err)
		}
		log.Printf("Created bucket: %s", bucketName)
	}

	// 演示1: 上传对象
	testData := []byte("Hello, MinIO with Erasure Coding! This is a test file content.")
	objectName := "test-object.txt"

	log.Println("\n=== 上传对象 ===")
	err = client.PutObject(ctx, bucketName, objectName,
		bytes.NewReader(testData),
		int64(len(testData)),
		minio.PutObjectOptions{
			UserMetadata: map[string]string{
				"custom-key": "custom-value",
			},
		})
	if err != nil {
		log.Fatalf("Failed to put object: %v", err)
	}

	// 演示2: 下载对象
	log.Println("\n=== 下载对象 ===")
	data, meta, err := client.GetObject(ctx, bucketName, objectName)
	if err != nil {
		log.Fatalf("Failed to get object: %v", err)
	}

	log.Printf("Downloaded data: %s", string(data))
	log.Printf("Metadata: Name=%s, Size=%d, ModTime=%s",
		meta.Name, meta.Size, meta.ModTime.Format(time.RFC3339))

	// 验证数据完整性
	if !bytes.Equal(data, testData) {
		log.Fatal("Data mismatch!")
	}
	log.Println("✓ Data integrity verified!")
}
