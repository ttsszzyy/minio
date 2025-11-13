# MinIO 擦除码客户端 SDK 演示

## 概述

这是一个完整的 Go 客户端 SDK 演示,展示如何与简化版的 MinIO 服务器交互,在客户端完成擦除编解码,并通过 HTTP Header 传递 xl.meta。

## 核心流程

### 上传流程

```
原始数据 (64 bytes)
    ↓
Reed-Solomon 编码 (4数据分片 + 2校验分片)
    ↓
编码后数据 (96 bytes)
    ↓
生成 xl.meta (FileInfo)
    ↓
序列化 xl.meta (msgp)
    ↓
Base64 编码
    ↓
放入 HTTP Header: X-Minio-XL-Meta
    ↓
上传到服务器
```

### 下载流程

```
从服务器下载编码数据
    ↓
从 HTTP Response Header 读取 X-Minio-XL-Meta
    ↓
Base64 解码
    ↓
反序列化 xl.meta (msgp)
    ↓
Reed-Solomon 解码
    ↓
恢复原始数据
```

## 关键代码

### 1. xl.meta 通过 HTTP Header 传递

**上传时:**
```go
// 序列化 xl.meta
xlMetaBytes, _ := xlMeta.MarshalMsg(nil)

// Base64 编码
xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaBytes)

// 放入 HTTP Header
opts.UserMetadata["X-Minio-XL-Meta"] = xlMetaEncoded

// 上传
client.PutObject(ctx, bucket, object, reader, size, opts)
```

**下载时:**
```go
// 下载对象
object, _ := client.GetObject(ctx, bucket, object, opts)
stat, _ := object.Stat()

// 从 Header 中提取 xl.meta
xlMetaEncoded := stat.Metadata.Get("X-Minio-Xl-Meta")

// Base64 解码
xlMetaBytes, _ := base64.StdEncoding.DecodeString(xlMetaEncoded)

// 反序列化
var xlMeta FileInfo
xlMeta.UnmarshalMsg(xlMetaBytes)
```

### 2. Reed-Solomon 擦除编码

**编码 (4数据+2校验):**
```go
enc, _ := reedsolomon.New(4, 2) // 可容忍2个分片丢失

// 分片
shards := make([][]byte, 6)
for i := 0; i < 4; i++ {
    shards[i] = data[i*shardSize : (i+1)*shardSize]
}

// 生成校验分片
enc.Encode(shards)

// 连接所有分片
encodedData := append(shards[0], shards[1]...)...
```

**解码:**
```go
enc, _ := reedsolomon.New(4, 2)

// 分片
shards := make([][]byte, 6)
// ... 从编码数据中分离分片 ...

// 重建(如果有损坏)
enc.Reconstruct(shards)

// 合并数据分片
data := append(shards[0], shards[1], shards[2], shards[3])
```

## 安装依赖

```bash
cd examples/erasure-client-sdk
go mod tidy
```

主要依赖:
- `github.com/minio/minio-go/v7` - MinIO Go SDK
- `github.com/klauspost/reedsolomon` - Reed-Solomon 纠删码库
- `github.com/tinylib/msgp` - MessagePack 序列化

## 运行示例

### 1. 启动 MinIO 服务器

```bash
# 单端点
./minio server /data

# 多端点(负载均衡)
./minio server /data1 /data2 /data3 /data4
```

### 2. 运行客户端

```bash
cd examples/erasure-client-sdk
go run main.go
```

### 预期输出

```
Created bucket: test-bucket

=== 上传对象 ===
Successfully uploaded test-object.txt (original: 64 bytes, encoded: 96 bytes, ratio: 150.00%)

=== 下载对象 ===
Successfully downloaded test-object.txt (encoded: 96 bytes, decoded: 64 bytes)
Downloaded data: Hello, MinIO with Erasure Coding! This is a test file content.
Metadata: Name=test-object.txt, Size=64, ModTime=2024-01-01T12:00:00Z
✓ Data integrity verified!
```

## FileInfo 结构体

```go
type FileInfo struct {
    Name      string            `msg:"name"`       // 对象名称
    Size      int64             `msg:"size"`       // 原始大小
    ModTime   time.Time         `msg:"modtime"`    // 修改时间
    VersionID string            `msg:"version_id"` // 版本ID
    Metadata  map[string]string `msg:"metadata"`   // 用户元数据
}
```

**序列化方法:**
- 使用 MessagePack (msgp) 格式
- 二进制编码,比 JSON 更紧凑高效
- 需要使用 `msgp` 工具生成序列化代码

### 生成 msgp 代码

```bash
# 安装 msgp
go install github.com/tinylib/msgp@latest

# 生成序列化代码
msgp -file=main.go -o=fileinfo_gen.go
```

## API 使用示例

### 上传对象

```go
client, _ := NewErasureClient("localhost:9000", "minioadmin", "minioadmin", 4, 2)

data := []byte("Hello, World!")
err := client.PutObject(ctx, "bucket", "object.txt", 
    bytes.NewReader(data), 
    int64(len(data)),
    minio.PutObjectOptions{
        UserMetadata: map[string]string{
            "author": "John Doe",
        },
    })
```

### 下载对象

```go
data, meta, err := client.GetObject(ctx, "bucket", "object.txt")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Data: %s\n", string(data))
fmt.Printf("Size: %d bytes\n", meta.Size)
fmt.Printf("ModTime: %s\n", meta.ModTime)
```

## 擦除码配置

### 常见配置

| 数据分片 | 校验分片 | 总分片 | 容错能力 | 存储开销 |
| -------- | -------- | ------ | -------- | -------- |
| 4        | 2        | 6      | 2个分片  | 150%     |
| 8        | 4        | 12     | 4个分片  | 150%     |
| 10       | 4        | 14     | 4个分片  | 140%     |
| 12       | 4        | 16     | 4个分片  | 133%     |

**选择建议:**
- 小文件: 4+2 (快速编解码)
- 大文件: 10+4 或 12+4 (更高效率)
- 高可靠: 增加校验分片数

## HTTP Header 详情

### 请求 Header (上传)

```http
PUT /bucket/object.txt HTTP/1.1
Host: localhost:9000
Authorization: AWS4-HMAC-SHA256 ...
Content-Type: application/octet-stream
Content-Length: 96
X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoidGVzdC1vYmplY3QudHh0Ii...

[编码后的数据 96 bytes]
```

### 响应 Header (下载)

```http
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Length: 96
X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoidGVzdC1vYmplY3QudHh0Ii...
ETag: "abc123..."

[编码后的数据 96 bytes]
```

## 与标准 MinIO 客户端对比

| 特性         | 标准 MinIO 客户端 | 擦除码客户端      |
| ------------ | ----------------- | ----------------- |
| 擦除编码     | 服务器端          | 客户端            |
| 上传数据     | 原始数据          | 编码数据          |
| 下载数据     | 原始数据          | 编码数据+解码     |
| 元数据传递   | 标准字段          | xl.meta in Header |
| 服务器存储   | 分片存储          | 完整存储          |
| 客户端复杂度 | 低                | 高                |
| 存储效率     | ~70%              | 100%(服务器端)    |

## 性能考虑

### 编码开销
- CPU: Reed-Solomon 编码需要 CPU 计算
- 内存: 需要缓冲完整数据
- 时间: 小文件影响小,大文件明显

### 优化建议
1. **分块编码**: 大文件分块编码,避免占用大量内存
2. **并发编码**: 多个块并发编码
3. **缓存编码**: 相同数据可复用编码结果
4. **流式处理**: 使用流式编解码减少内存占用

## 错误处理

```go
// 上传失败
err := client.PutObject(...)
if err != nil {
    if minio.ToErrorResponse(err).Code == "NoSuchBucket" {
        // 创建 bucket
        client.client.MakeBucket(...)
    }
}

// 下载失败
data, meta, err := client.GetObject(...)
if err != nil {
    if minio.ToErrorResponse(err).Code == "NoSuchKey" {
        // 对象不存在
    }
}

// xl.meta 缺失
if stat.Metadata.Get("X-Minio-Xl-Meta") == "" {
    // 对象不是擦除编码的
}
```

## 完整示例

参见 `main.go` 文件,包含:
- ✅ ErasureClient 完整实现
- ✅ Reed-Solomon 编解码
- ✅ xl.meta 序列化/反序列化
- ✅ HTTP Header 传递
- ✅ 上传下载演示
- ✅ 数据完整性验证

## 生产环境建议

### 1. FileInfo 序列化
使用 msgp 工具生成高效的序列化代码:
```bash
msgp -file=fileinfo.go -o=fileinfo_gen.go
```

### 2. 错误处理
- 检查所有错误返回
- 实现重试机制
- 记录详细日志

### 3. 性能优化
- 大文件使用分块编码
- 实现连接池
- 启用 HTTP/2

### 4. 安全性
- 使用 HTTPS
- 验证 xl.meta 签名
- 限制 xl.meta 大小

## 总结

这个 SDK 演示了:
1. ✅ 如何在客户端实现 Reed-Solomon 擦除编码
2. ✅ 如何生成和序列化 xl.meta
3. ✅ 如何通过 HTTP Header `X-Minio-XL-Meta` 传递元数据
4. ✅ 完整的上传下载流程
5. ✅ 与简化版 MinIO 服务器的完美配合

**核心要点**: xl.meta 通过 Base64 编码后放在 HTTP Header 中传递,这样不影响标准 S3 API 兼容性。
