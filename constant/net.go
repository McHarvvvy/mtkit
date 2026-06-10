package constant

// ProtocolTCP 是 daemon 监听与客户端探活使用的网络协议名。
const ProtocolTCP = "tcp"

// HeaderToken 是 daemon 控制面（lease 与 faked control）统一的 token 鉴权请求头。
// 置于 leaf 包：lease 客户端、faked rule 客户端、daemon 服务端都引用同一来源，避免多处字面量漂移。
const HeaderToken = "X-Mtkit-Token"
