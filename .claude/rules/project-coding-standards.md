---
trigger: always_on
---

### 1. 错误处理
基于 git-codecommit.us-east-1.amazonaws.com/v1/repos/mssiot_proto.git/msserror 包构建统一的错误处理机制。
标准错误码定义：msserror/err_code.go
HTTP 接口错误码和映射关系定义：msserror/api_code.go
#### 全新创建错误
```
// 创建新错误（Error 级别）
msserror.{ErrorType}.ErrorNew("error message")
msserror.{ErrorType}.ErrorNewf("error message with %s", "param")
// 创建新错误（Info 级别，仅用于参数校验类）
msserror.{ErrorType}.InfoNew("parameter validation message")
msserror.{ErrorType}.InfoNewf("parameter %s is invalid", "param")
// 创建新错误（Warn 级别）
msserror.{ErrorType}.WarnNew("warning message")
msserror.{ErrorType}.WarnNewf("warning: %s", "detail")
```
#### 包装已有错误
```
// 包装已有错误（保留原始错误链）
msserror.{ErrorType}.ErrorWrap(err, "context message")
msserror.{ErrorType}.ErrorWrapf(err, "context: %s, param: %d", "value", 123)
// Warn 级别包装
msserror.{ErrorType}.WarnWrap(err, "warning context")
```
#### 添加上下文
```
// 仅添加上下文信息，不改变错误类型
msserror.AddContext(err, "additional context")
msserror.AddContextf(err, "context with param: %s", "value")
```
#### 错误类型判断
```
// 获取错误类型进行比较
errType := msserror.GetErrType(err)
if errType == msserror.TdEngineErr {
    // 处理数据库错误
}
// 获取 HTTP 错误码用于响应
httpCode := msserror.GetHttpCode(errType)
httpMsg := msserror.GetHttpErrMsg(httpCode)
```
#### 统一响应
```
// pkg/response/res_result.go 中的标准实现
func (res *Result) SendFailure(ctx *gin.Context, err error) {
    errType := msserror.GetErrType(err)
    res.ApiStatus = msserror.GetHttpCode(errType)
    res.Info = msserror.GetHttpErrMsg(res.ApiStatus)
    ctx.AbortWithStatusJSON(http.StatusOK, res)
}
```
#### 禁止事项
- 禁止忽略错误：所有 error 返回值必须处理
- 禁止使用 fmt.Errorf：统一使用 msserror 包
- 禁止使用 panic：除 Recovery 中间件外，不得使用 panic
- 禁止在 Controller 直接暴露内部错误：必须通过 SendFailure 统一响应
- 禁止过早处理错误：错误应向上传播到合适层级处理
#### 最佳实践
- 错误链完整性：使用 AddContext / ErrorWrap 保留原始错误链
- 上下文丰富性：错误消息应包含足够的调试信息（表名、参数值等）
- 类型精确性：选择最精确的错误类型；若没有合适的，应主动提议新建

### 2. 日志处理
基于 git-codecommit.us-east-1.amazonaws.com/v1/repos/mssiot_proto.git/logger 包构建统一的日志记录机制。
logger 与 msserror 关系紧密
#### 处理规则
- **统一使用 `OutputLogRaw`**：禁止直接调用 `Error`、`Warn`、`Info`、`Debug` 底层方法
- **error 必须是 `msserror.SysErr`**：日志级别由 `msserror.LogLevel` 决定
- **traceId 必须传递**：通过 `util.GetContextTraceId(ctx)` 获取
- **在处理错误的地方进行日志处理**，同一个错误不能重复日志记录
- **mkey 使用常量**：**必须**使用 `constant/logger_key.go` 中定义的常量；如有必要可以提议新建 mkey 标识
- **Access 日志由中间件自动记录**：业务层不要手动调用 `HttpAccess`/`GrpcAccess`
#### 典型输出场景
| 场景 | 推荐方法 | 说明 |
|------|----------|------|
| **业务错误** | `OutputLogRaw(err, traceId, mkey)` | 统一入口，自动根据 `msserror.LogLevel` 分级输出 |
| **HTTP 请求日志** | `HttpAccess(...)` | 由 `LogHttpAccessMiddleware` 自动调用 |
| **gRPC 请求日志** | `GrpcAccess(...)` | 由 `LogGrpcAccessMiddleware` 自动调用 |
| **Panic 恢复** | `OutputLogRaw(apiErr, traceId, mkey)` | 在 Recovery 中间件中使用 |
#### 集成 msserror
```go
// 正确用法: msserror 携带日志级别
deps.Logger.OutputLogRaw(
    msserror.SystemErr.ErrorWrap(err, "context message"), // Error 级别
    util.GetContextTraceId(ctx),
    constant.HistorageKey,
)

deps.Logger.OutputLogRaw(
    msserror.InvalidParameter.InfoNew("param validation failed"), // Info 级别
    util.GetContextTraceId(ctx),
    constant.HistorageKey,
)
```
#### 禁止事项
- ❌ 禁止使用 `fmt.Println` 或 `log.Println`
- ❌ 禁止忽略错误不记录日志
- ❌ 禁止在 controller 层以外直接调用 Access 日志
- ❌ 禁止 traceId 为空字符串（除非是启动阶段）

### 3. UID 和 GUID
- 新增需求中，与系统外实体对接统一使用 GUID 标识用户；系统内统一使用 UID，并且存储 UID；
- 两者调用统一方法转换
- 旧代码如遇违反该原则的，若没有明确的修改指令，保持原样

### 4. int64 和 time.Time
- 新增需求中,涉及与系统外实体对接统一使用 int64 形式表示时间；系统内统一使用 time.Time 表示时间
- 对外场景涉及：http & mqtt 协议，数据库 & 缓存 等
- 旧代码如遇违反该原则的，若没有明确的修改指令，保持原样