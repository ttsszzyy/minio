# MinIO 擦除码移除 - 文件修改清单

## 文件操作清单

### 需要删除的文件 (擦除码核心)

```bash
# 擦除码编解码
cmd/erasure-coding.go
cmd/erasure-encode.go
cmd/erasure-decode.go
cmd/erasure-encode_test.go
cmd/erasure-decode_test.go
cmd/erasure_test.go

# 擦除码对象层
cmd/erasure-object.go
cmd/erasure-sets.go
cmd/erasure-server-pool.go
cmd/erasure-server-pool-decom.go
cmd/erasure-server-pool-rebalance.go
cmd/erasure-server-pool-decom_gen.go
cmd/erasure-server-pool-decom_test.go
cmd/erasure-server-pool-rebalance_gen_test.go

# 擦除码修复
cmd/erasure-healing.go
cmd/erasure-healing-common.go
cmd/erasure-healing_test.go
cmd/erasure-healing-common_test.go
cmd/erasure-heal_test.go

# 擦除码分片上传
cmd/erasure-multipart.go

# 擦除码元数据
cmd/erasure-metadata.go
cmd/erasure-metadata_test.go
cmd/erasure-metadata-utils.go

# 擦除码错误
cmd/erasure-errors.go

# 后台修复任务
cmd/background-heal-ops.go
cmd/background-newdisks-heal-ops.go
cmd/background-newdisks-heal-ops_gen.go
cmd/background-newdisks-heal-ops_gen_test.go

# 管理接口(擦除码相关)
cmd/admin-heal-ops.go
```

### 需要创建的新文件

```bash
# 简化对象层
cmd/simple-object.go          # 替代 erasure-object.go
cmd/simple-storage.go         # 简化的存储接口实现
cmd/simple-multipart.go       # 简化的分片上传
```

### 需要重点修改的文件

#### 1. 核心存储层
```bash
cmd/xl-storage.go             # 简化为单文件存储
cmd/xl-storage-disk-id-check.go
cmd/xl-storage-format-v1.go  # 简化格式定义
cmd/xl-storage-format-v2.go
cmd/xl-storage-free-version.go
```

#### 2. API 处理层
```bash
cmd/object-handlers.go        # 修改PutObject/GetObject
cmd/object-multipart-handlers.go  # 简化分片上传
cmd/api-router.go            # 可能需要调整路由
```

#### 3. 对象接口
```bash
cmd/object-api-interface.go  # ObjectLayer接口定义
cmd/object-api-datatypes.go  # 数据类型简化
cmd/object-api-utils.go      # 工具函数简化
```

#### 4. 服务器初始化
```bash
cmd/server-main.go           # 初始化逻辑
cmd/prepare-storage.go       # 存储准备
cmd/format-erasure.go        # 需要简化或删除
```

#### 5. 元数据处理
```bash
cmd/xl-storage-meta.go       # xl.meta处理
cmd/fs-v1-metadata.go        # 元数据格式
```

### 需要审查和可能修改的文件

#### 管理接口
```bash
cmd/admin-handlers.go
cmd/admin-handlers-pools.go  # 移除pool相关
cmd/admin-server-info.go
```

#### 复制相关
```bash
cmd/bucket-replication.go    # 需要适配,但保留
cmd/bucket-replication-handlers.go
cmd/bucket-replication-utils.go
```

#### 生命周期
```bash
cmd/bucket-lifecycle.go      # 保留但可能需要调整
cmd/bucket-lifecycle-handlers.go
```

#### 对象锁
```bash
cmd/bucket-object-lock.go    # 保留
```

#### 版本控制
```bash
cmd/bucket-versioning.go     # 需要简化
cmd/versioning-handler.go
```

### 配置文件

```bash
internal/config/storageclass/  # 存储类配置,可能需要调整
internal/config/heal/          # 修复配置,删除
```

### 测试文件

```bash
cmd/erasure-*.go              # 所有擦除码测试,删除
cmd/object-api-*_test.go      # 需要更新测试用例
cmd/server_test.go            # 更新集成测试
```

## 详细修改指南

### cmd/xl-storage.go 修改重点

```go
// 删除的功能:
- 多磁盘协调
- 擦除码分片写入
- bitrot校验(可选保留)

// 保留的功能:
- 基本文件I/O
- xl.meta读写
- 目录操作

// 新增的功能:
- 完整对象读写
- xl.meta接收存储
```

### cmd/object-handlers.go 修改重点

```go
// PutObjectHandler 改动:
1. 添加: 读取 X-Minio-XL-Meta header
2. 修改: 直接调用存储层,无编码
3. 添加: 保存xl.meta到磁盘

// GetObjectHandler 改动:
1. 修改: 直接读取文件,无解码
2. 添加: 读取xl.meta返回给客户端
3. 简化: 移除擦除码相关逻辑

// CopyObjectHandler 改动:
1. 简化为文件复制
2. 复制xl.meta文件
```

### cmd/object-multipart-handlers.go 修改重点

```go
// NewMultipartUpload:
- 删除: 擦除码初始化
- 保留: 创建上传会话

// PutObjectPart:
- 修改: 直接存储分片,无编码
- 保留: 分片元数据记录

// CompleteMultipartUpload:
- 修改: 直接合并分片文件
- 添加: 接收xl.meta
- 删除: 擦除码重组逻辑
```

### cmd/server-main.go 修改重点

```go
// serverMain:
原: globalObjectAPI, err = newErasureServerPools(ctx, endpointServerPools)
改: globalObjectAPI, err = newSimpleStorage(ctx, endpointServerPools)

// 删除:
- 擦除码自检
- 多池初始化
- 修复任务初始化

// 保留:
- 基本服务器配置
- 认证初始化
- API路由设置
```

## 依赖关系图

```
删除文件影响分析:

erasure-coding.go
  ├── erasure-encode.go (依赖)
  ├── erasure-decode.go (依赖)
  └── erasure-object.go (依赖)
        ├── erasure-sets.go (依赖)
        ├── erasure-server-pool.go (依赖)
        └── object-handlers.go (引用)

解决方案: 创建 simple-object.go 替代整个链条
```

## 修改优先级

### 高优先级 (必须先完成)
1. ✅ 创建 simple-object.go
2. ✅ 修改 xl-storage.go
3. ✅ 修改 server-main.go
4. ✅ 修改 object-api-interface.go

### 中优先级
5. ✅ 修改 object-handlers.go
6. ✅ 修改 object-multipart-handlers.go
7. ✅ 删除擦除码核心文件
8. ✅ 更新测试用例

### 低优先级
9. ⬜ 更新文档
10. ⬜ 优化性能
11. ⬜ 清理遗留代码
12. ⬜ 适配管理接口

## 快速开始脚本

```bash
#!/bin/bash

# 1. 创建工作分支
git checkout -b remove-erasure-code

# 2. 创建新文件
touch cmd/simple-object.go
touch cmd/simple-storage.go
touch cmd/simple-multipart.go

# 3. 备份关键文件
cp cmd/object-handlers.go cmd/object-handlers.go.bak
cp cmd/xl-storage.go cmd/xl-storage.go.bak
cp cmd/server-main.go cmd/server-main.go.bak

# 4. 删除擦除码文件 (先注释,确认后再删)
# git rm cmd/erasure-*.go
# git rm cmd/background-heal-ops.go

# 5. 提交初始更改
git add .
git commit -m "Phase 1: Create simple storage structure"
```

## 验证检查清单

### 编译检查
```bash
# 检查编译错误
go build ./cmd/...

# 检查导入错误
go mod tidy
```

### 功能检查
```bash
# 1. 启动服务器
./minio server /data

# 2. 测试上传
mc cp testfile.txt myminio/bucket/

# 3. 测试下载
mc cp myminio/bucket/testfile.txt ./downloaded.txt

# 4. 验证文件
diff testfile.txt downloaded.txt

# 5. 测试xl.meta
# 检查 /data/bucket/testfile.txt/xl.meta 是否存在
```

### 性能检查
```bash
# 对比改造前后性能
minio speedtest --obj-size 1MB --duration 60s
```

## 回滚方案

```bash
# 如果出现问题,快速回滚
git checkout main
git branch -D remove-erasure-code

# 或者保留分支但重置
git reset --hard HEAD~10
```

## 进度跟踪

### 阶段1: 核心存储 (第1-2周)
- [ ] 创建 simple-object.go (80% 完成)
- [ ] 修改 xl-storage.go (60% 完成)
- [ ] 修改 server-main.go (40% 完成)
- [ ] 基本测试通过

### 阶段2: API层 (第3-4周)
- [ ] 修改 object-handlers.go (70% 完成)
- [ ] 修改 multipart-handlers.go (50% 完成)
- [ ] xl.meta传输机制 (30% 完成)
- [ ] API测试通过

### 阶段3: 清理 (第5周)
- [ ] 删除擦除码文件 (0% 完成)
- [ ] 更新测试用例 (20% 完成)
- [ ] 文档更新 (0% 完成)

### 阶段4: 优化 (第6周)
- [ ] 性能优化
- [ ] 错误处理完善
- [ ] 边界情况处理

## 注意事项

1. **保持向后兼容**: S3 API接口保持不变
2. **xl.meta格式**: 与客户端约定清晰的格式
3. **错误处理**: 完善的错误提示
4. **日志记录**: 添加详细日志便于调试
5. **测试覆盖**: 每个功能都要有测试用例

## 获取帮助

如果在实施过程中遇到问题:
1. 参考 ERASURE_REMOVAL_PLAN.md 了解整体方案
2. 参考 IMPLEMENTATION_GUIDE.md 查看代码示例
3. 查看原有代码理解现有逻辑
4. 逐步实施,每步都充分测试

---

**开始实施前务必**:
1. ✅ 完整备份代码
2. ✅ 创建独立开发分支
3. ✅ 理解现有架构
4. ✅ 准备测试环境
5. ✅ 与客户端团队对齐协议
