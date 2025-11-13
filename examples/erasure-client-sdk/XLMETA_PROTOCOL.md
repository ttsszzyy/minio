# xl.meta 传递机制详解

## 核心机制

**xl.meta 通过 HTTP Header `X-Amz-Meta-X-Minio-Xl-Meta` 传递**

## 完整流程图

```
客户端上传流程:
┌─────────────────────────────────────────────────────────────┐
│ 1. 原始数据: "Hello World" (11 bytes)                        │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. Reed-Solomon 编码 (4+2)                                   │
│    数据分片: [D1, D2, D3, D4]                                │
│    校验分片: [P1, P2]                                        │
│    编码后: 16 bytes                                          │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. 生成 xl.meta (FileInfo)                                   │
│    {                                                         │
│      "name": "hello.txt",                                    │
│      "size": 11,                                             │
│      "modtime": "2024-11-13T12:00:00Z",                      │
│      "metadata": {"author": "John"}                          │
│    }                                                         │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. 序列化 (MessagePack/JSON)                                 │
│    二进制: [0x81, 0xa4, 0x6e, 0x61, ...]                    │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. Base64 编码                                               │
│    "eyJuYW1lIjoiaGVsbG8udHh0Iiwic2l6ZSI6MTF9..."             │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. HTTP 请求                                                 │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ PUT /bucket/hello.txt HTTP/1.1                          │ │
│ │ Host: localhost:9000                                    │ │
│ │ Authorization: AWS4-HMAC-SHA256 ...                     │ │
│ │ X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoiaGVsbG8...      │ │
│ │ Content-Length: 16                                      │ │
│ │                                                         │ │
│ │ [16 bytes encoded data]                                 │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 7. MinIO 服务器接收                                          │
│    - 从 Header 提取 xl.meta                                  │
│    - Base64 解码                                             │
│    - 存储到 /bucket/hello.txt/xl.meta                        │
│    - 存储编码数据到 /bucket/hello.txt/data                   │
└─────────────────────────────────────────────────────────────┘


客户端下载流程:
┌─────────────────────────────────────────────────────────────┐
│ 1. HTTP 请求                                                 │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ GET /bucket/hello.txt HTTP/1.1                          │ │
│ │ Host: localhost:9000                                    │ │
│ │ Authorization: AWS4-HMAC-SHA256 ...                     │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. MinIO 服务器响应                                          │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ HTTP/1.1 200 OK                                         │ │
│ │ X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoiaGVsbG8...      │ │
│ │ Content-Length: 16                                      │ │
│ │                                                         │ │
│ │ [16 bytes encoded data]                                 │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. 从 Header 提取 xl.meta                                    │
│    xlMetaEncoded = response.Header.Get("X-Amz-Meta-...")    │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. Base64 解码                                               │
│    xlMetaBytes = base64.Decode(xlMetaEncoded)                │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. 反序列化 xl.meta                                          │
│    var xlMeta FileInfo                                       │
│    xlMeta.UnmarshalMsg(xlMetaBytes)                          │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. Reed-Solomon 解码                                         │
│    使用 xlMeta.Size 知道原始大小                             │
│    解码: 16 bytes → 11 bytes                                 │
└─────────────────────────────────────────────────────────────┘
                           ↓
┌─────────────────────────────────────────────────────────────┐
│ 7. 返回原始数据: "Hello World" (11 bytes)                    │
└─────────────────────────────────────────────────────────────┘
```

## 代码对应关系

### 客户端上传

```go
// main.go 第 68-91 行
func (ec *ErasureClient) PutObject(...) error {
    // 步骤 1-2: 读取并编码
    data, _ := io.ReadAll(reader)
    encodedData, _ := ec.encodeData(data)
    
    // 步骤 3: 生成 xl.meta
    xlMeta := &FileInfo{
        Name:    objectName,
        Size:    int64(len(data)),
        ModTime: time.Now(),
    }
    
    // 步骤 4-5: 序列化并 Base64 编码
    xlMetaBytes, _ := xlMeta.MarshalMsg(nil)
    xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaBytes)
    
    // 步骤 6: 放入 Header
    opts.UserMetadata["X-Minio-XL-Meta"] = xlMetaEncoded
    
    // 步骤 7: 上传
    ec.client.PutObject(ctx, bucket, object, 
        bytes.NewReader(encodedData), ...)
}
```

### 服务器接收

```go
// cmd/simple-object.go 第 164-177 行
func (s *simpleObjects) PutObject(...) (ObjectInfo, error) {
    // 从 Header 提取 xl.meta
    xlMetaEncoded := opts.UserDefined["X-Minio-XL-Meta"]
    
    // Base64 解码
    xlMetaBytes, _ := base64.StdEncoding.DecodeString(xlMetaEncoded)
    
    // 存储数据
    storage.WriteAll(ctx, bucket, dataPath, dataBytes)
    
    // 存储 xl.meta
    storage.WriteAll(ctx, bucket, metaPath, xlMetaBytes)
}
```

### 服务器响应

```go
// cmd/simple-object.go 第 232-252 行
func (s *simpleObjects) GetObjectNInfo(...) (*GetObjectReader, error) {
    // 读取 xl.meta
    xlMetaData, _ := storage.ReadAll(ctx, bucket, metaPath)
    
    // Base64 编码
    xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaData)
    
    // 放入响应 Header
    objInfo.UserDefined["X-Minio-XL-Meta"] = xlMetaEncoded
    
    // 返回编码数据流
    reader, _ := storage.ReadFileStream(ctx, bucket, dataPath, ...)
    return &GetObjectReader{Reader: reader, ObjInfo: objInfo}, nil
}
```

### 客户端下载

```go
// main.go 第 96-133 行
func (ec *ErasureClient) GetObject(...) ([]byte, *FileInfo, error) {
    // 下载
    object, _ := ec.client.GetObject(ctx, bucket, object, ...)
    stat, _ := object.Stat()
    
    // 从 Header 提取 xl.meta
    xlMetaEncoded := stat.Metadata.Get("X-Minio-Xl-Meta")
    
    // Base64 解码
    xlMetaBytes, _ := base64.StdEncoding.DecodeString(xlMetaEncoded)
    
    // 反序列化
    var xlMeta FileInfo
    xlMeta.UnmarshalMsg(xlMetaBytes)
    
    // 读取并解码
    encodedData, _ := io.ReadAll(object)
    decodedData, _ := ec.decodeData(encodedData, int(xlMeta.Size))
    
    return decodedData, &xlMeta, nil
}
```

## Header 名称规范

### MinIO Go SDK 自动处理

MinIO Go SDK 会自动处理自定义 Header:
- 客户端: `X-Minio-XL-Meta` → SDK → `X-Amz-Meta-X-Minio-Xl-Meta`
- 服务器: `X-Amz-Meta-X-Minio-Xl-Meta` → SDK → `X-Minio-Xl-Meta`

### 兼容性

- ✅ 完全兼容 S3 API
- ✅ 不影响标准 S3 客户端
- ✅ 使用标准的 `X-Amz-Meta-*` 前缀

## 数据流示意图

```
┌──────────┐     编码数据 + xl.meta in Header      ┌──────────┐
│          │  ─────────────────────────────────────▶ │          │
│  Client  │                                         │  Server  │
│          │  ◀─────────────────────────────────────  │          │
└──────────┘     编码数据 + xl.meta in Header      └──────────┘
```

## 关键优势

1. **标准兼容**: 使用标准 HTTP Header,不修改 S3 API
2. **无侵入性**: 对现有代码影响最小
3. **灵活性**: Header 大小足够,可扩展
4. **透明性**: 可通过工具查看 Header 内容
5. **高效性**: 避免额外的请求获取元数据

## 实际示例

### 使用 curl 查看

```bash
# 上传(带 xl.meta)
curl -X PUT http://localhost:9000/bucket/object \
  -H "X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoi..." \
  --data-binary @encoded-data.bin

# 下载(查看 xl.meta)
curl -I http://localhost:9000/bucket/object
# 输出包含:
# X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoi...
```

### Header 大小限制

- 大多数 HTTP 服务器: 8KB Header 限制
- xl.meta 典型大小: 200-500 bytes
- Base64 编码后: 300-700 bytes
- ✅ 完全在安全范围内

## 总结

**核心机制**: xl.meta 序列化后 Base64 编码,通过 HTTP Header `X-Amz-Meta-X-Minio-Xl-Meta` 在客户端和服务器间传递。

这种设计:
- ✅ 简单可靠
- ✅ 标准兼容
- ✅ 易于实现
- ✅ 易于调试
- ✅ 性能优秀
