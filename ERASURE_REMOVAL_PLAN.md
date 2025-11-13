# MinIO 擦除码移除改造方案

## 目标
将MinIO服务器中的擦除码逻辑完全移除,保留纯粹的S3 API上传下载功能,擦除编解码由客户端完成,客户端负责生成xl.meta文件。

## 架构变更概述

### 当前架构
```
客户端 → MinIO Server → 擦除编码层 → 存储层(多磁盘分片)
                         ↓
                    xl.meta生成
```

### 目标架构
```
客户端(擦除编码 + xl.meta生成) → MinIO Server(简化版) → 存储层(单文件存储)
```

## 核心变更点

### 1. 存储层简化 (cmd/xl-storage.go)

#### 变更内容:
- 移除擦除码分片逻辑
- 简化为直接文件存储
- 保留xl.meta接收和存储功能
- 移除多磁盘协调逻辑

#### 关键修改:
```go
// 原: 多分片写入
func (s *xlStorage) WriteAll(...)
  → 写入到多个分片文件

// 改: 单文件写入
func (s *xlStorage) WriteAll(...)
  → 直接写入完整对象数据
  → 接收客户端传来的xl.meta并保存
```

### 2. 对象层简化 (cmd/erasure-object.go → cmd/simple-object.go)

#### 需要创建新的简化对象层:
```go
// 替代 erasureObjects
type simpleObjects struct {
    storage StorageAPI
}
```

#### 核心功能:
- `PutObject`: 直接存储客户端上传的完整对象数据和xl.meta
- `GetObject`: 直接读取完整对象返回
- `DeleteObject`: 删除对象文件和xl.meta
- `ListObjects`: 遍历文件系统列出对象

### 3. 移除擦除码相关文件

#### 完全移除:
- `cmd/erasure-encode.go` - 编码逻辑
- `cmd/erasure-decode.go` - 解码逻辑
- `cmd/erasure-coding.go` - 核心擦除码
- `cmd/erasure-healing.go` - 修复逻辑
- `cmd/erasure-sets.go` - 擦除集管理
- `cmd/erasure-server-pool.go` - 服务器池管理
- `cmd/erasure-multipart.go` - 分片上传擦除逻辑
- `cmd/erasure-metadata.go` - 擦除码元数据

#### 保留但简化:
- `cmd/xl-storage.go` - 简化为单文件存储
- `cmd/xl-storage-format.go` - 简化格式定义

### 4. API处理层修改 (cmd/object-handlers.go)

#### PutObjectHandler 修改:
```go
func (api objectAPIHandlers) PutObjectHandler(w http.ResponseWriter, r *http.Request) {
    // 1. 接收客户端上传的对象数据
    objectData := r.Body
    
    // 2. 接收客户端传来的xl.meta (通过自定义header)
    xlMeta := r.Header.Get("X-Minio-XL-Meta")
    
    // 3. 直接存储到存储层,无擦除编码
    objInfo, err := objectAPI.PutObject(ctx, bucket, object, 
        &PutObjReader{Reader: objectData},
        opts)
    
    // 4. 保存xl.meta文件
    saveXLMeta(bucket, object, xlMeta)
}
```

#### GetObjectHandler 修改:
```go
func (api objectAPIHandlers) getObjectHandler(...) {
    // 1. 直接读取完整对象文件
    gr, err := objectAPI.GetObjectNInfo(ctx, bucket, object, rs, h, opts)
    
    // 2. 可选: 读取xl.meta返回给客户端
    xlMeta := loadXLMeta(bucket, object)
    w.Header().Set("X-Minio-XL-Meta", xlMeta)
    
    // 3. 流式传输数据
    io.Copy(w, gr)
}
```

### 5. 分片上传简化 (cmd/object-multipart-handlers.go)

#### NewMultipartUpload:
- 移除擦除编码初始化
- 仅创建上传会话

#### PutObjectPart:
- 直接存储分片数据,无编码
- 等待所有分片上传完成

#### CompleteMultipartUpload:
- 直接合并分片文件
- 接收客户端生成的xl.meta
- 无需擦除码处理

### 6. 元数据结构简化

#### xl.meta 结构调整:
```go
// 保留基本元数据,移除擦除码信息
type FileInfo struct {
    Name      string
    Size      int64
    ModTime   time.Time
    Metadata  map[string]string
    // 移除以下字段:
    // Erasure   ErasureInfo  ← 删除
    // Parts     []ObjectPartInfo ← 简化
}
```

### 7. 配置和初始化修改

#### cmd/server-main.go:
```go
// 原: 初始化擦除码服务器池
newErasureServerPools(...)

// 改: 初始化简单存储
newSimpleStorage(...)
```

#### 移除配置项:
- 擦除码数据/校验块配置
- 多磁盘集配置
- 修复和重平衡配置

## 详细实施步骤

### 阶段1: 准备和分析 (2-3天)
1. 完整代码审计,标记所有擦除码相关代码
2. 设计新的简化存储架构
3. 定义客户端-服务器协议(xl.meta传输)
4. 准备测试用例

### 阶段2: 核心存储层改造 (5-7天)
1. 创建 `cmd/simple-storage.go` - 新的存储实现
2. 修改 `cmd/xl-storage.go` - 简化文件操作
3. 实现单文件读写逻辑
4. 实现xl.meta接收和存储

### 阶段3: 对象层改造 (5-7天)
1. 创建 `cmd/simple-object.go` 替代 erasure-object.go
2. 实现简化版 PutObject, GetObject, DeleteObject
3. 实现简化版 ListObjects
4. 实现简化版 CopyObject

### 阶段4: API层适配 (3-5天)
1. 修改 `cmd/object-handlers.go` 中的处理函数
2. 添加xl.meta传输机制(HTTP Header)
3. 修改分片上传处理逻辑
4. 更新错误处理

### 阶段5: 移除遗留代码 (2-3天)
1. 删除擦除码相关文件
2. 清理无用的导入和引用
3. 移除相关配置选项
4. 更新文档

### 阶段6: 测试和验证 (5-7天)
1. 单元测试
2. 集成测试
3. 性能测试
4. 客户端兼容性测试

## 关键技术点

### 1. xl.meta 传输机制
```go
// 上传时,客户端在HTTP Header中传递xl.meta
PUT /bucket/object
X-Minio-XL-Meta: base64(xl.meta内容)
Content-Length: 对象大小

// 服务器接收并保存
xlMetaBytes := base64.Decode(r.Header.Get("X-Minio-XL-Meta"))
saveXLMetaFile(bucket, object, xlMetaBytes)
```

### 2. 文件存储结构
```
磁盘根目录/
  ├── buckets/
  │   └── my-bucket/
  │       └── my-object/
  │           ├── data        # 完整对象数据
  │           └── xl.meta     # 元数据文件(客户端生成)
```

### 3. 对象读取流程
```go
// 1. 读取xl.meta验证
xlMeta := readXLMeta(bucket, object)

// 2. 直接读取数据文件
dataFile := openDataFile(bucket, object)

// 3. 流式返回
io.Copy(responseWriter, dataFile)
```

### 4. 版本控制简化
- 保留版本ID概念
- 使用文件系统目录管理版本
- 每个版本独立存储

## 需要保留的功能

1. **S3 API兼容性**: 完整的S3协议支持
2. **认证鉴权**: IAM, 存储桶策略等
3. **加密**: SSE-C, SSE-S3, SSE-KMS
4. **对象锁**: 保留期,合规模式
5. **生命周期**: 过期删除,转换
6. **版本控制**: 对象版本管理
7. **事件通知**: 对象操作通知
8. **复制**: 跨区域/站点复制(但无需擦除码)

## 性能影响分析

### 优势:
- **简化架构**: 无编解码开销
- **更快读写**: 直接文件操作
- **更低延迟**: 减少计算层
- **易于调试**: 简单的存储结构

### 劣势:
- **无数据冗余**: 依赖客户端实现
- **无自动修复**: 服务器端不做数据修复
- **存储效率**: 客户端需要管理冗余数据

## 风险和注意事项

1. **兼容性风险**: 
   - 客户端必须配合更新
   - 需要定义清晰的xl.meta格式协议

2. **数据可靠性**:
   - 完全依赖客户端擦除码实现质量
   - 服务器无法验证数据完整性

3. **迁移挑战**:
   - 现有MinIO部署无法直接升级
   - 需要数据迁移工具

4. **运维复杂度**:
   - 客户端逻辑更复杂
   - 问题排查需要客户端配合

## 估计工作量

- **开发**: 20-25 个工作日
- **测试**: 10-15 个工作日
- **文档**: 5-7 个工作日
- **总计**: 35-47 个工作日 (7-9周)

## 成功标准

1. ✅ 服务器不执行任何擦除编解码操作
2. ✅ 能接收和存储客户端生成的xl.meta
3. ✅ 完整的S3 API支持(上传/下载/删除/列表)
4. ✅ 分片上传正常工作
5. ✅ 通过所有功能测试用例
6. ✅ 性能满足预期(无擦除码开销)
7. ✅ 客户端能够正确交互

## 下一步行动

1. **确认方案**: 技术评审,确认改造方案
2. **准备环境**: 搭建开发和测试环境
3. **开始编码**: 按阶段实施改造
4. **持续测试**: 每个阶段完成后测试
5. **文档更新**: 同步更新API文档

---

**注意**: 这是一个重大架构变更,建议:
- 在新分支上进行开发
- 保持原有代码分支用于对比
- 充分测试后再考虑合并
- 准备回退方案
