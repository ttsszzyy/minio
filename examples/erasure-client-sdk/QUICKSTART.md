# 快速开始

## 1. 启动 MinIO 服务器

```bash
# 单端点模式
cd /Users/yangyang/Documents/GitHub/minio
GOWORK=off go build -o /tmp/minio
/tmp/minio server /tmp/minio-data
```

或者多端点模式:
```bash
/tmp/minio server /tmp/data{1...4}
```

## 2. 运行客户端示例

```bash
cd examples/erasure-client-sdk

# 下载依赖
go mod tidy

# 运行
go run main.go
```

## 3. 预期输出

```
Created bucket: test-bucket

=== 上传对象 ===
Successfully uploaded test-object.txt (original: 64 bytes, encoded: 96 bytes, ratio: 150.00%)

=== 下载对象 ===
Successfully downloaded test-object.txt (encoded: 96 bytes, decoded: 64 bytes)
Downloaded data: Hello, MinIO with Erasure Coding! This is a test file content.
Metadata: Name=test-object.txt, Size=64, ModTime=2024-11-13T12:00:00Z
✓ Data integrity verified!
```

## 核心代码片段

### 上传对象 (xl.meta 通过 Header 传递)

```go
// 1. 擦除编码
encodedData, _ := encodeData(originalData)

// 2. 生成 xl.meta
xlMeta := &FileInfo{
    Name:    "object.txt",
    Size:    int64(len(originalData)),
    ModTime: time.Now(),
}

// 3. 序列化并 Base64 编码
xlMetaBytes, _ := xlMeta.MarshalMsg(nil)
xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaBytes)

// 4. 放入 Header
opts.UserMetadata["X-Minio-XL-Meta"] = xlMetaEncoded

// 5. 上传
client.PutObject(ctx, bucket, object, bytes.NewReader(encodedData), size, opts)
```

### 下载对象 (从 Header 读取 xl.meta)

```go
// 1. 下载
object, _ := client.GetObject(ctx, bucket, object, opts)
stat, _ := object.Stat()

// 2. 从 Header 读取 xl.meta
xlMetaEncoded := stat.Metadata.Get("X-Minio-Xl-Meta")

// 3. Base64 解码
xlMetaBytes, _ := base64.StdEncoding.DecodeString(xlMetaEncoded)

// 4. 反序列化
var xlMeta FileInfo
xlMeta.UnmarshalMsg(xlMetaBytes)

// 5. 读取编码数据
encodedData, _ := io.ReadAll(object)

// 6. 擦除解码
originalData, _ := decodeData(encodedData, int(xlMeta.Size))
```

## HTTP Header 示例

### 上传请求

```http
PUT /test-bucket/test-object.txt HTTP/1.1
Host: localhost:9000
X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoidGVzdC1vYmplY3QudHh0IiwicWl6ZSI6NjQsIm1vZHRpbWUiOiIyMDI0LTExLTEzVDEyOjAwOjAwWiJ9
Content-Length: 96

[96 bytes of encoded data]
```

### 下载响应

```http
HTTP/1.1 200 OK
X-Amz-Meta-X-Minio-Xl-Meta: eyJuYW1lIjoidGVzdC1vYmplY3QudHh0IiwicWl6ZSI6NjQsIm1vZHRpbWUiOiIyMDI0LTExLTEzVDEyOjAwOjAwWiJ9
Content-Length: 96

[96 bytes of encoded data]
```

## 配置说明

### 修改 MinIO 服务器地址

编辑 `main.go`:
```go
endpoint := "localhost:9000"  // 改为你的服务器地址
accessKey := "minioadmin"     // 改为你的 Access Key
secretKey := "minioadmin"     // 改为你的 Secret Key
```

### 修改擦除码配置

```go
// 4个数据分片 + 2个校验分片 (容忍2个分片丢失, 150%存储开销)
client, _ := NewErasureClient(endpoint, accessKey, secretKey, 4, 2)

// 或者使用其他配置:
// 8+4: 容忍4个分片丢失, 150%存储开销
// 10+4: 容忍4个分片丢失, 140%存储开销
// 12+4: 容忍4个分片丢失, 133%存储开销
```

## 常见问题

### Q1: 无法连接到服务器
```
Failed to check bucket: dial tcp [::1]:9000: connect: connection refused
```

**解决**: 确保 MinIO 服务器正在运行
```bash
/tmp/minio server /tmp/minio-data
```

### Q2: xl.meta not found
```
xl.meta not found in response headers
```

**解决**: 确保使用的是简化版的 MinIO 服务器(包含 simple-object.go)

### Q3: 数据不匹配
```
Data mismatch!
```

**解决**: 检查擦除码配置是否一致(上传和下载使用相同的配置)

## 下一步

- 查看 `README.md` 了解更多细节
- 修改 `main.go` 测试更多功能
- 集成到你的项目中
