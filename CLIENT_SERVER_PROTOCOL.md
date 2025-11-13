# MinIO 客户端-服务器协议规范 (无擦除码版本)

## 概述

本文档定义了MinIO服务器去除擦除码后,客户端与服务器之间的数据交互协议。客户端负责擦除编码和xl.meta文件生成,服务器仅负责存储和检索。

## xl.meta 文件格式

### 基本结构

```go
type XLMetaV2 struct {
    Version    int      `json:"version"`    // 固定为 2
    Format     string   `json:"format"`     // "xl"
    ObjectInfo ObjectMetadata `json:"object"`
}

type ObjectMetadata struct {
    // 基本信息
    Name       string    `json:"name"`
    Size       int64     `json:"size"`
    ModTime    time.Time `json:"modTime"`
    
    // 擦除码信息 (由客户端填充)
    Erasure    ErasureInfo `json:"erasure"`
    
    // 用户元数据
    Metadata   map[string]string `json:"metadata"`
    
    // 分片信息 (如果是分片上传)
    Parts      []PartInfo `json:"parts,omitempty"`
    
    // 校验信息
    Checksum   string `json:"checksum"`
}

type ErasureInfo struct {
    Algorithm     string `json:"algorithm"`      // 例如: "ReedSolomon"
    DataBlocks    int    `json:"dataBlocks"`     // 数据块数量
    ParityBlocks  int    `json:"parityBlocks"`   // 校验块数量
    BlockSize     int64  `json:"blockSize"`      // 块大小
    Distribution  []int  `json:"distribution"`   // 分片分布 (客户端编码后的索引)
    Checksums     []string `json:"checksums"`    // 每个分片的校验和
}

type PartInfo struct {
    PartNumber int    `json:"partNumber"`
    Size       int64  `json:"size"`
    ETag       string `json:"etag"`
}
```

### 示例 xl.meta (JSON格式)

```json
{
  "version": 2,
  "format": "xl",
  "object": {
    "name": "myfile.dat",
    "size": 10485760,
    "modTime": "2024-11-13T10:30:00Z",
    "erasure": {
      "algorithm": "ReedSolomon",
      "dataBlocks": 4,
      "parityBlocks": 2,
      "blockSize": 1048576,
      "distribution": [1, 2, 3, 4, 5, 6],
      "checksums": [
        "d41d8cd98f00b204e9800998ecf8427e",
        "098f6bcd4621d373cade4e832627b4f6",
        "5d41402abc4b2a76b9719d911017c592",
        "7c211433f02071597741e6ff5a8ea34f",
        "0cc175b9c0f1b6a831c399e269772661",
        "92eb5ffee6ae2fec3ad71c777531578f"
      ]
    },
    "metadata": {
      "Content-Type": "application/octet-stream",
      "X-Amz-Meta-User": "alice"
    },
    "checksum": "9b71d224bd62f3785d96d46ad3ea3d73"
  }
}
```

## HTTP 协议扩展

### 新增 HTTP Header

#### X-Minio-XL-Meta
- **用途**: 传输xl.meta文件内容
- **格式**: Base64编码的JSON字符串
- **方向**: 双向 (客户端→服务器, 服务器→客户端)
- **用于**: PutObject, GetObject, CompleteMultipartUpload

#### X-Minio-Erasure-Info (可选)
- **用途**: 快速传输擦除码参数
- **格式**: `algorithm:dataBlocks:parityBlocks:blockSize`
- **示例**: `ReedSolomon:4:2:1048576`
- **方向**: 客户端→服务器

#### X-Minio-Client-Version
- **用途**: 标识客户端版本,确保协议兼容
- **格式**: `minio-client/version`
- **示例**: `minio-client/v2.0-no-erasure`
- **方向**: 客户端→服务器

## API 操作详解

### 1. PutObject (单次上传)

#### 客户端流程:
1. 对原始数据进行擦除编码
2. 生成xl.meta文件
3. 将xl.meta转为Base64
4. 发送HTTP请求,包含xl.meta header

#### HTTP 请求示例:
```http
PUT /bucket/myfile.dat HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
Content-Type: application/octet-stream
Content-Length: 10485760
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjoyLCJmb3JtYXQiOiJ4bCIsIm9iamVjdCI6ey...
X-Minio-Client-Version: minio-client/v2.0-no-erasure

[已编码的对象数据]
```

#### 服务器处理:
1. 接收并解析 X-Minio-XL-Meta header
2. 验证xl.meta格式
3. 存储对象数据到: `/bucket/myfile.dat/data`
4. 存储xl.meta到: `/bucket/myfile.dat/xl.meta`
5. 返回成功响应

#### HTTP 响应示例:
```http
HTTP/1.1 200 OK
ETag: "9b71d224bd62f3785d96d46ad3ea3d73"
X-Minio-XL-Meta-Stored: true
Content-Length: 0
```

### 2. GetObject (下载)

#### 客户端流程:
1. 发送GET请求
2. 接收对象数据和xl.meta
3. 根据xl.meta进行擦除解码(如果需要)
4. 返回原始数据

#### HTTP 请求示例:
```http
GET /bucket/myfile.dat HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
X-Minio-Client-Version: minio-client/v2.0-no-erasure
```

#### 服务器处理:
1. 读取对象数据: `/bucket/myfile.dat/data`
2. 读取xl.meta: `/bucket/myfile.dat/xl.meta`
3. 将xl.meta转为Base64添加到响应header
4. 流式返回数据

#### HTTP 响应示例:
```http
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Length: 10485760
ETag: "9b71d224bd62f3785d96d46ad3ea3d73"
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjoyLCJmb3JtYXQiOiJ4bCIsIm9iamVjdCI6ey...
Last-Modified: Wed, 13 Nov 2024 10:30:00 GMT

[对象数据]
```

### 3. Multipart Upload (分片上传)

#### 3.1 NewMultipartUpload

**请求:**
```http
POST /bucket/largefile.dat?uploads HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
```

**响应:**
```http
HTTP/1.1 200 OK
Content-Type: application/xml

<?xml version="1.0" encoding="UTF-8"?>
<InitiateMultipartUploadResult>
   <Bucket>bucket</Bucket>
   <Key>largefile.dat</Key>
   <UploadId>VXBsb2FkIElEIGZvciA2aWWpbmcncyBteS1tb3ZpZS5tMnRzIHVwbG9hZA</UploadId>
</InitiateMultipartUploadResult>
```

#### 3.2 UploadPart

**客户端流程:**
1. 将大文件分割为多个部分
2. 对每个部分进行擦除编码
3. 上传每个编码后的部分

**请求:**
```http
PUT /bucket/largefile.dat?partNumber=1&uploadId=VXBsb2... HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
Content-Length: 5242880

[分片数据 - 已编码]
```

**响应:**
```http
HTTP/1.1 200 OK
ETag: "098f6bcd4621d373cade4e832627b4f6"
```

#### 3.3 CompleteMultipartUpload

**客户端流程:**
1. 合并所有分片的元数据
2. 生成完整的xl.meta
3. 发送完成请求,包含xl.meta

**请求:**
```http
POST /bucket/largefile.dat?uploadId=VXBsb2... HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
Content-Type: application/xml
X-Minio-XL-Meta: eyJ2ZXJzaW9uIjoyLCJmb3JtYXQiOiJ4bCIsIm9iamVjdCI6ey...

<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUpload>
   <Part>
      <PartNumber>1</PartNumber>
      <ETag>098f6bcd4621d373cade4e832627b4f6</ETag>
   </Part>
   <Part>
      <PartNumber>2</PartNumber>
      <ETag>5d41402abc4b2a76b9719d911017c592</ETag>
   </Part>
</CompleteMultipartUpload>
```

**服务器处理:**
1. 验证所有分片存在
2. 合并分片到最终对象
3. 保存xl.meta
4. 清理临时分片
5. 返回成功

**响应:**
```http
HTTP/1.1 200 OK
Content-Type: application/xml

<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUploadResult>
   <Location>http://minio.example.com/bucket/largefile.dat</Location>
   <Bucket>bucket</Bucket>
   <Key>largefile.dat</Key>
   <ETag>9b71d224bd62f3785d96d46ad3ea3d73</ETag>
</CompleteMultipartUploadResult>
```

### 4. CopyObject (对象复制)

#### 请求:
```http
PUT /dest-bucket/newfile.dat HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
X-Amz-Copy-Source: /source-bucket/oldfile.dat
```

#### 服务器处理:
1. 复制源对象数据
2. 复制源对象xl.meta
3. 可选: 客户端提供新的xl.meta覆盖

#### 响应:
```http
HTTP/1.1 200 OK
Content-Type: application/xml

<?xml version="1.0" encoding="UTF-8"?>
<CopyObjectResult>
   <LastModified>2024-11-13T10:30:00.000Z</LastModified>
   <ETag>9b71d224bd62f3785d96d46ad3ea3d73</ETag>
</CopyObjectResult>
```

### 5. DeleteObject (删除对象)

#### 请求:
```http
DELETE /bucket/myfile.dat HTTP/1.1
Host: minio.example.com
Authorization: AWS4-HMAC-SHA256 ...
```

#### 服务器处理:
1. 删除 `/bucket/myfile.dat/data`
2. 删除 `/bucket/myfile.dat/xl.meta`
3. 删除 `/bucket/myfile.dat/` 目录

#### 响应:
```http
HTTP/1.1 204 No Content
```

## 客户端实现示例

### Python 客户端示例

```python
import boto3
import base64
import json
from reedsolomon import RSCodec

class MinIONoErasureClient:
    def __init__(self, endpoint, access_key, secret_key):
        self.s3 = boto3.client(
            's3',
            endpoint_url=endpoint,
            aws_access_key_id=access_key,
            aws_secret_access_key=secret_key
        )
        self.rs_codec = RSCodec(2)  # 2个校验块
        
    def put_object(self, bucket, key, data):
        # 1. 擦除编码
        encoded_data = self.rs_codec.encode(data)
        
        # 2. 生成xl.meta
        xl_meta = {
            "version": 2,
            "format": "xl",
            "object": {
                "name": key,
                "size": len(data),
                "modTime": datetime.now().isoformat(),
                "erasure": {
                    "algorithm": "ReedSolomon",
                    "dataBlocks": 4,
                    "parityBlocks": 2,
                    "blockSize": len(data) // 4,
                },
                "metadata": {},
                "checksum": hashlib.md5(data).hexdigest()
            }
        }
        
        # 3. Base64编码xl.meta
        xl_meta_b64 = base64.b64encode(
            json.dumps(xl_meta).encode()
        ).decode()
        
        # 4. 上传
        self.s3.put_object(
            Bucket=bucket,
            Key=key,
            Body=encoded_data,
            Metadata={
                'x-minio-xl-meta': xl_meta_b64,
                'x-minio-client-version': 'python-client/v1.0'
            }
        )
    
    def get_object(self, bucket, key):
        # 1. 下载对象
        response = self.s3.get_object(Bucket=bucket, Key=key)
        encoded_data = response['Body'].read()
        
        # 2. 获取xl.meta
        xl_meta_b64 = response['Metadata'].get('x-minio-xl-meta')
        if xl_meta_b64:
            xl_meta = json.loads(
                base64.b64decode(xl_meta_b64).decode()
            )
        
        # 3. 擦除解码
        original_data = self.rs_codec.decode(encoded_data)[0]
        
        return original_data, xl_meta
```

### Go 客户端示例

```go
package main

import (
    "encoding/base64"
    "encoding/json"
    "github.com/minio/minio-go/v7"
    "github.com/klauspost/reedsolomon"
)

type MinIONoErasureClient struct {
    client    *minio.Client
    rsEncoder reedsolomon.Encoder
}

func NewClient(endpoint, accessKey, secretKey string) (*MinIONoErasureClient, error) {
    client, err := minio.New(endpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
        Secure: true,
    })
    if err != nil {
        return nil, err
    }
    
    // 4+2擦除码
    rsEncoder, err := reedsolomon.New(4, 2)
    if err != nil {
        return nil, err
    }
    
    return &MinIONoErasureClient{
        client:    client,
        rsEncoder: rsEncoder,
    }, nil
}

func (c *MinIONoErasureClient) PutObject(ctx context.Context, bucket, key string, data []byte) error {
    // 1. 擦除编码
    shards, err := c.rsEncoder.Split(data)
    if err != nil {
        return err
    }
    err = c.rsEncoder.Encode(shards)
    if err != nil {
        return err
    }
    
    // 合并所有分片
    var encodedData []byte
    for _, shard := range shards {
        encodedData = append(encodedData, shard...)
    }
    
    // 2. 生成xl.meta
    xlMeta := XLMetaV2{
        Version: 2,
        Format:  "xl",
        Object: ObjectMetadata{
            Name:    key,
            Size:    int64(len(data)),
            ModTime: time.Now(),
            Erasure: ErasureInfo{
                Algorithm:    "ReedSolomon",
                DataBlocks:   4,
                ParityBlocks: 2,
                BlockSize:    int64(len(data) / 4),
            },
        },
    }
    
    xlMetaJSON, _ := json.Marshal(xlMeta)
    xlMetaB64 := base64.StdEncoding.EncodeToString(xlMetaJSON)
    
    // 3. 上传
    _, err = c.client.PutObject(ctx, bucket, key, 
        bytes.NewReader(encodedData),
        int64(len(encodedData)),
        minio.PutObjectOptions{
            UserMetadata: map[string]string{
                "X-Minio-Xl-Meta":        xlMetaB64,
                "X-Minio-Client-Version": "go-client/v1.0",
            },
        })
    
    return err
}

func (c *MinIONoErasureClient) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
    // 1. 下载对象
    obj, err := c.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
    if err != nil {
        return nil, err
    }
    defer obj.Close()
    
    encodedData, err := io.ReadAll(obj)
    if err != nil {
        return nil, err
    }
    
    // 2. 获取xl.meta
    stat, err := obj.Stat()
    if err != nil {
        return nil, err
    }
    
    xlMetaB64 := stat.UserMetadata["X-Minio-Xl-Meta"]
    // 解析xl.meta...
    
    // 3. 擦除解码
    shardSize := len(encodedData) / 6
    shards := make([][]byte, 6)
    for i := 0; i < 6; i++ {
        shards[i] = encodedData[i*shardSize : (i+1)*shardSize]
    }
    
    err = c.rsEncoder.Reconstruct(shards)
    if err != nil {
        return nil, err
    }
    
    // 合并数据分片
    var originalData []byte
    for i := 0; i < 4; i++ {
        originalData = append(originalData, shards[i]...)
    }
    
    return originalData, nil
}
```

## 错误处理

### 服务器端错误码

| 错误码                | HTTP状态码 | 描述                       | 解决方案               |
| --------------------- | ---------- | -------------------------- | ---------------------- |
| XLMetaMissing         | 400        | 缺少X-Minio-XL-Meta header | 客户端上传时必须提供   |
| XLMetaInvalid         | 400        | xl.meta格式无效            | 检查JSON格式和必需字段 |
| XLMetaVersionMismatch | 400        | xl.meta版本不支持          | 更新客户端或服务器版本 |
| ErasureInfoIncomplete | 400        | 擦除码信息不完整           | 确保所有擦除码参数存在 |

### 客户端错误处理

```python
try:
    client.put_object('bucket', 'key', data)
except XLMetaError as e:
    logger.error(f"xl.meta error: {e}")
    # 重新生成xl.meta
except ErasureCodeError as e:
    logger.error(f"Erasure encoding error: {e}")
    # 重新编码
```

## 兼容性

### 版本兼容性矩阵

| 客户端版本 | 服务器版本 | 兼容性     | 说明             |
| ---------- | ---------- | ---------- | ---------------- |
| v2.0+      | v2.0+      | ✅ 完全兼容 | 新协议           |
| v1.x       | v2.0+      | ❌ 不兼容   | 旧客户端不支持   |
| v2.0+      | v1.x       | ❌ 不兼容   | 旧服务器有擦除码 |

### 迁移指南

从旧版MinIO迁移到无擦除码版本:
1. 导出所有对象
2. 客户端重新编码
3. 上传到新服务器
4. 验证数据完整性

## 性能优化建议

1. **并行上传**: 分片上传时并行上传多个分片
2. **压缩**: 在擦除编码前压缩数据
3. **缓存xl.meta**: 客户端缓存常用对象的xl.meta
4. **流式处理**: 大文件使用流式编码,避免内存占用

## 安全考虑

1. **xl.meta完整性**: 服务器应验证xl.meta签名
2. **传输加密**: 使用HTTPS保护xl.meta传输
3. **访问控制**: xl.meta可能包含敏感信息,需要鉴权

---

本协议版本: v2.0  
最后更新: 2024-11-13
