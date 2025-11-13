# MinIO 擦除码移除 - 实施指南

## 核心代码修改示例

### 1. 创建简化存储层

#### 新文件: cmd/simple-object.go

```go
// Copyright (c) 2024 MinIO, Inc.
// Simple Object Storage - No Erasure Coding

package cmd

import (
    "context"
    "io"
    "path"
)

// simpleObjects - 简化的对象存储实现,无擦除码
type simpleObjects struct {
    storage StorageAPI
}

// PutObject - 直接存储对象,无擦除编码
func (s *simpleObjects) PutObject(ctx context.Context, bucket, object string, 
    data *PutObjReader, opts ObjectOptions) (ObjectInfo, error) {
    
    // 1. 验证输入
    if err := checkPutObjectArgs(ctx, bucket, object); err != nil {
        return ObjectInfo{}, err
    }
    
    // 2. 准备元数据
    metadata := opts.UserDefined
    if metadata == nil {
        metadata = make(map[string]string)
    }
    
    // 3. 从header中提取客户端生成的xl.meta
    xlMetaEncoded := opts.UserDefined["X-Minio-XL-Meta"]
    var xlMetaBytes []byte
    if xlMetaEncoded != "" {
        var err error
        xlMetaBytes, err = base64.StdEncoding.DecodeString(xlMetaEncoded)
        if err != nil {
            return ObjectInfo{}, err
        }
        delete(opts.UserDefined, "X-Minio-XL-Meta")
    }
    
    // 4. 生成对象路径
    dataPath := pathJoin(bucket, object, "data")
    metaPath := pathJoin(bucket, object, "xl.meta")
    
    // 5. 直接写入对象数据 (无擦除编码)
    n, err := s.storage.WriteAll(ctx, dataPath, data)
    if err != nil {
        return ObjectInfo{}, err
    }
    
    // 6. 保存xl.meta (客户端生成的)
    if len(xlMetaBytes) > 0 {
        err = s.storage.WriteAll(ctx, metaPath, xlMetaBytes)
        if err != nil {
            // 清理已写入的数据
            s.storage.Delete(ctx, dataPath)
            return ObjectInfo{}, err
        }
    } else {
        // 如果客户端没提供xl.meta,生成一个基本的
        fi := FileInfo{
            Name:     object,
            Size:     n,
            ModTime:  time.Now(),
            Metadata: metadata,
        }
        xlMetaBytes, _ = fi.Marshal()
        s.storage.WriteAll(ctx, metaPath, xlMetaBytes)
    }
    
    // 7. 返回对象信息
    return ObjectInfo{
        Bucket:      bucket,
        Name:        object,
        Size:        n,
        ModTime:     time.Now(),
        UserDefined: metadata,
    }, nil
}

// GetObjectNInfo - 直接读取对象,无解码
func (s *simpleObjects) GetObjectNInfo(ctx context.Context, bucket, object string, 
    rs *HTTPRangeSpec, h http.Header, opts ObjectOptions) (*GetObjectReader, error) {
    
    // 1. 验证对象存在
    objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
    if err != nil {
        return nil, err
    }
    
    // 2. 打开数据文件
    dataPath := pathJoin(bucket, object, "data")
    reader, err := s.storage.ReadFile(ctx, dataPath, 0, objInfo.Size)
    if err != nil {
        return nil, err
    }
    
    // 3. 处理Range请求
    if rs != nil {
        start, length := rs.Start, rs.End-rs.Start+1
        reader, err = s.storage.ReadFile(ctx, dataPath, start, length)
        if err != nil {
            return nil, err
        }
    }
    
    // 4. 读取xl.meta返回给客户端
    metaPath := pathJoin(bucket, object, "xl.meta")
    xlMetaBytes, _ := s.storage.ReadAll(ctx, metaPath)
    if len(xlMetaBytes) > 0 {
        objInfo.UserDefined["X-Minio-XL-Meta"] = base64.StdEncoding.EncodeToString(xlMetaBytes)
    }
    
    // 5. 构造返回对象
    gr := &GetObjectReader{
        ObjInfo: objInfo,
        pReader: reader,
    }
    
    return gr, nil
}

// GetObjectInfo - 获取对象元数据
func (s *simpleObjects) GetObjectInfo(ctx context.Context, bucket, object string, 
    opts ObjectOptions) (ObjectInfo, error) {
    
    // 1. 读取xl.meta
    metaPath := pathJoin(bucket, object, "xl.meta")
    xlMetaBytes, err := s.storage.ReadAll(ctx, metaPath)
    if err != nil {
        return ObjectInfo{}, err
    }
    
    // 2. 解析元数据
    var fi FileInfo
    if err := fi.Unmarshal(xlMetaBytes); err != nil {
        return ObjectInfo{}, err
    }
    
    // 3. 转换为ObjectInfo
    return ObjectInfo{
        Bucket:      bucket,
        Name:        object,
        Size:        fi.Size,
        ModTime:     fi.ModTime,
        UserDefined: fi.Metadata,
    }, nil
}

// DeleteObject - 删除对象和元数据
func (s *simpleObjects) DeleteObject(ctx context.Context, bucket, object string, 
    opts ObjectOptions) (ObjectInfo, error) {
    
    // 1. 获取对象信息
    objInfo, err := s.GetObjectInfo(ctx, bucket, object, opts)
    if err != nil {
        return ObjectInfo{}, err
    }
    
    // 2. 删除数据文件
    dataPath := pathJoin(bucket, object, "data")
    if err := s.storage.Delete(ctx, dataPath); err != nil {
        return ObjectInfo{}, err
    }
    
    // 3. 删除元数据文件
    metaPath := pathJoin(bucket, object, "xl.meta")
    s.storage.Delete(ctx, metaPath)
    
    // 4. 删除目录
    objPath := pathJoin(bucket, object)
    s.storage.Delete(ctx, objPath)
    
    return objInfo, nil
}

// ListObjects - 列出对象
func (s *simpleObjects) ListObjects(ctx context.Context, bucket, prefix, marker, 
    delimiter string, maxKeys int) (ListObjectsInfo, error) {
    
    var result ListObjectsInfo
    result.Objects = make([]ObjectInfo, 0)
    
    // 遍历存储目录
    entries, err := s.storage.ListDir(ctx, bucket, prefix, maxKeys)
    if err != nil {
        return result, err
    }
    
    for _, entry := range entries {
        if entry == "" {
            continue
        }
        
        // 读取对象信息
        objInfo, err := s.GetObjectInfo(ctx, bucket, entry, ObjectOptions{})
        if err != nil {
            continue
        }
        
        result.Objects = append(result.Objects, objInfo)
    }
    
    return result, nil
}
```

### 2. 修改API处理函数

#### 修改: cmd/object-handlers.go

```go
// PutObjectHandler - 接收客户端上传的对象和xl.meta
func (api objectAPIHandlers) PutObjectHandler(w http.ResponseWriter, r *http.Request) {
    ctx := newContext(r, w, "PutObject")
    
    // ... 现有的验证逻辑保持不变 ...
    
    objectAPI := api.ObjectAPI()
    if objectAPI == nil {
        writeErrorResponse(ctx, w, errorCodes.ToAPIErr(ErrServerNotInitialized), r.URL)
        return
    }
    
    // 关键修改: 从Header中提取xl.meta
    xlMeta := r.Header.Get("X-Minio-XL-Meta")
    if xlMeta != "" {
        if opts.UserDefined == nil {
            opts.UserDefined = make(map[string]string)
        }
        opts.UserDefined["X-Minio-XL-Meta"] = xlMeta
    }
    
    // 创建reader
    size := r.ContentLength
    pReader := NewPutObjReader(rawReader)
    
    // 关键: 直接调用PutObject,内部不做擦除编码
    objInfo, err := objectAPI.PutObject(ctx, bucket, object, pReader, opts)
    if err != nil {
        writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
        return
    }
    
    // 在响应中返回xl.meta (如果需要)
    if xlMeta != "" {
        w.Header().Set("X-Minio-XL-Meta-Stored", "true")
    }
    
    // 返回成功响应
    writeSuccessResponseHeadersOnly(w)
}

// GetObjectHandler - 修改以返回xl.meta
func (api objectAPIHandlers) GetObjectHandler(w http.ResponseWriter, r *http.Request) {
    ctx := newContext(r, w, "GetObject")
    
    // ... 现有逻辑 ...
    
    // 获取对象
    gr, err := objectAPI.GetObjectNInfo(ctx, bucket, object, rs, r.Header, opts)
    if err != nil {
        writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
        return
    }
    defer gr.Close()
    
    // 关键: 如果UserDefined中有xl.meta,返回给客户端
    if xlMeta, ok := gr.ObjInfo.UserDefined["X-Minio-XL-Meta"]; ok {
        w.Header().Set("X-Minio-XL-Meta", xlMeta)
    }
    
    // 设置响应头
    setObjectHeaders(w, gr.ObjInfo, nil, opts)
    
    // 流式传输数据 (无解码)
    httpWriter := xioutil.WriteOnClose(w)
    io.Copy(httpWriter, gr)
}
```

### 3. 修改分片上传

#### 修改: cmd/object-multipart-handlers.go

```go
// NewMultipartUploadHandler - 简化版,无擦除码初始化
func (api objectAPIHandlers) NewMultipartUploadHandler(w http.ResponseWriter, r *http.Request) {
    ctx := newContext(r, w, "NewMultipartUpload")
    
    // ... 验证逻辑 ...
    
    // 关键: 直接创建上传会话,不初始化擦除码
    uploadID := mustGetUUID()
    
    // 创建上传元数据目录
    uploadPath := pathJoin(minioMetaTmpBucket, bucket, object, uploadID)
    err := objectAPI.storage.MkdirAll(ctx, uploadPath, 0755)
    if err != nil {
        writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
        return
    }
    
    // 返回uploadID
    response := generateInitiateMultipartUploadResponse(bucket, object, uploadID)
    writeSuccessResponseXML(w, encodeResponse(response))
}

// PutObjectPartHandler - 直接存储分片,无编码
func (api objectAPIHandlers) PutObjectPartHandler(w http.ResponseWriter, r *http.Request) {
    ctx := newContext(r, w, "PutObjectPart")
    
    // ... 验证逻辑 ...
    
    partNumber, _ := strconv.Atoi(vars["partNumber"])
    
    // 关键: 直接写入分片数据,不做擦除编码
    partPath := pathJoin(minioMetaTmpBucket, bucket, object, uploadID, 
        fmt.Sprintf("part.%d", partNumber))
    
    n, err := objectAPI.storage.WriteAll(ctx, partPath, r.Body)
    if err != nil {
        writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
        return
    }
    
    // 保存分片元数据
    partInfo := PartInfo{
        PartNumber: partNumber,
        Size:       n,
        ETag:       r.Header.Get("Content-MD5"),
    }
    
    // 返回成功
    w.Header().Set("ETag", partInfo.ETag)
    writeSuccessResponseHeadersOnly(w)
}

// CompleteMultipartUploadHandler - 合并分片
func (api objectAPIHandlers) CompleteMultipartUploadHandler(w http.ResponseWriter, r *http.Request) {
    ctx := newContext(r, w, "CompleteMultipartUpload")
    
    // ... 验证逻辑 ...
    
    // 1. 接收客户端生成的xl.meta
    xlMeta := r.Header.Get("X-Minio-XL-Meta")
    
    // 2. 合并所有分片到最终对象
    uploadPath := pathJoin(minioMetaTmpBucket, bucket, object, uploadID)
    objectPath := pathJoin(bucket, object, "data")
    
    // 打开目标文件
    destFile, err := objectAPI.storage.Create(ctx, objectPath)
    if err != nil {
        writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
        return
    }
    defer destFile.Close()
    
    // 依次读取并合并分片
    var totalSize int64
    for _, part := range completeParts {
        partPath := pathJoin(uploadPath, fmt.Sprintf("part.%d", part.PartNumber))
        partData, err := objectAPI.storage.ReadAll(ctx, partPath)
        if err != nil {
            writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
            return
        }
        
        n, err := destFile.Write(partData)
        if err != nil {
            writeErrorResponse(ctx, w, toAPIError(ctx, err), r.URL)
            return
        }
        totalSize += int64(n)
        
        // 删除临时分片
        objectAPI.storage.Delete(ctx, partPath)
    }
    
    // 3. 保存xl.meta
    if xlMeta != "" {
        xlMetaBytes, _ := base64.StdEncoding.DecodeString(xlMeta)
        metaPath := pathJoin(bucket, object, "xl.meta")
        objectAPI.storage.WriteAll(ctx, metaPath, xlMetaBytes)
    }
    
    // 4. 清理上传目录
    objectAPI.storage.Delete(ctx, uploadPath)
    
    // 5. 返回成功响应
    response := generateCompleteMultpartUploadResponse(bucket, object, "", "")
    writeSuccessResponseXML(w, encodeResponse(response))
}
```

### 4. 存储层简化

#### 修改: cmd/xl-storage.go

```go
// WriteAll - 直接写入完整数据,无分片
func (s *xlStorage) WriteAll(ctx context.Context, filePath string, data io.Reader) (int64, error) {
    // 1. 创建目录
    dir := path.Dir(filePath)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return 0, err
    }
    
    // 2. 创建文件
    absPath := pathJoin(s.drivePath, filePath)
    f, err := os.Create(absPath)
    if err != nil {
        return 0, err
    }
    defer f.Close()
    
    // 3. 直接写入数据
    n, err := io.Copy(f, data)
    if err != nil {
        return 0, err
    }
    
    // 4. 同步到磁盘
    if err := f.Sync(); err != nil {
        return 0, err
    }
    
    return n, nil
}

// ReadFile - 直接读取文件,无解码
func (s *xlStorage) ReadFile(ctx context.Context, filePath string, offset, length int64) (io.ReadCloser, error) {
    absPath := pathJoin(s.drivePath, filePath)
    
    // 打开文件
    f, err := os.Open(absPath)
    if err != nil {
        return nil, err
    }
    
    // 定位到offset
    if offset > 0 {
        _, err = f.Seek(offset, io.SeekStart)
        if err != nil {
            f.Close()
            return nil, err
        }
    }
    
    // 如果指定了length,使用LimitedReader
    if length > 0 {
        return &limitedReadCloser{
            Reader: io.LimitReader(f, length),
            Closer: f,
        }, nil
    }
    
    return f, nil
}

// ReadAll - 读取整个文件
func (s *xlStorage) ReadAll(ctx context.Context, filePath string) ([]byte, error) {
    absPath := pathJoin(s.drivePath, filePath)
    return os.ReadFile(absPath)
}

// Delete - 删除文件或目录
func (s *xlStorage) Delete(ctx context.Context, filePath string) error {
    absPath := pathJoin(s.drivePath, filePath)
    return os.RemoveAll(absPath)
}

// ListDir - 列出目录内容
func (s *xlStorage) ListDir(ctx context.Context, dirPath string, prefix string, maxKeys int) ([]string, error) {
    absPath := pathJoin(s.drivePath, dirPath)
    
    entries, err := os.ReadDir(absPath)
    if err != nil {
        return nil, err
    }
    
    var result []string
    for _, entry := range entries {
        if len(result) >= maxKeys {
            break
        }
        
        name := entry.Name()
        if prefix != "" && !strings.HasPrefix(name, prefix) {
            continue
        }
        
        result = append(result, name)
    }
    
    return result, nil
}
```

### 5. 初始化修改

#### 修改: cmd/server-main.go

```go
// serverMain - 简化版初始化
func serverMain(ctx *cli.Context) {
    // ... 现有的初始化逻辑 ...
    
    // 关键: 使用简化存储,不初始化擦除码
    // 原: newErasureServerPools(...)
    // 改:
    storage, err := newSimpleStorage(globalEndpoints)
    if err != nil {
        logger.Fatal(err, "Unable to initialize storage")
    }
    
    // 设置全局对象API
    globalObjectAPI = storage
    
    // ... 其余初始化 ...
}

// newSimpleStorage - 创建简化存储
func newSimpleStorage(endpoints Endpoints) (ObjectLayer, error) {
    // 只使用第一个端点作为存储位置
    if len(endpoints) == 0 {
        return nil, errors.New("no endpoints specified")
    }
    
    endpoint := endpoints[0]
    storage, err := newXLStorage(endpoint, false)
    if err != nil {
        return nil, err
    }
    
    // 返回简化对象层
    return &simpleObjects{
        storage: storage,
    }, nil
}
```

## 客户端协议规范

### 上传对象 (PutObject)

```http
PUT /bucket/object HTTP/1.1
Host: minio.example.com
Content-Length: 1048576
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjo... (base64编码的xl.meta)

[对象数据]
```

### 下载对象 (GetObject)

```http
GET /bucket/object HTTP/1.1
Host: minio.example.com

响应:
HTTP/1.1 200 OK
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjo... (base64编码的xl.meta)
Content-Length: 1048576

[对象数据]
```

### 分片上传 (Multipart)

1. 初始化:
```http
POST /bucket/object?uploads HTTP/1.1
```

2. 上传分片:
```http
PUT /bucket/object?uploadId=xxx&partNumber=1 HTTP/1.1
[分片数据]
```

3. 完成上传:
```http
POST /bucket/object?uploadId=xxx HTTP/1.1
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjo... (base64编码的xl.meta)

<CompleteMultipartUpload>
  <Part><PartNumber>1</PartNumber><ETag>...</ETag></Part>
  ...
</CompleteMultipartUpload>
```

## 编译和测试

```bash
# 1. 编译
cd /Users/yangyang/Documents/GitHub/minio
make build

# 2. 运行服务器
./minio server /data

# 3. 测试上传
# 客户端需要生成xl.meta并在header中传递
mc cp --attr "X-Minio-XL-Meta=..." file.txt myminio/bucket/

# 4. 测试下载
mc cp myminio/bucket/file.txt ./
```

## 下一步

1. 实现 `cmd/simple-object.go`
2. 修改 `cmd/object-handlers.go`
3. 调整 `cmd/xl-storage.go`
4. 更新 `cmd/server-main.go`
5. 删除擦除码相关文件
6. 编译测试
7. 编写客户端适配代码

---

**重要提示**: 
- 这些是核心代码示例,实际实现需要处理更多边界情况
- 建议分阶段实施,每个阶段充分测试
- 保持与现有S3 API的兼容性
- 准备完整的测试用例
