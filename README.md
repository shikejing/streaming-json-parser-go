# streaming-json-parser-go

面向超大 JSON 文档的流式解析与查询库。输入可以是远大于内存的文件或管道，
解析过程中逐条产出匹配结果，单次匹配的内存占用只与匹配值本身有关，
与文档整体规模无关。整条链路只在本进程内运行，无任何外部依赖。

## 快速开始

```go
q, _ := streamjson.ParseQuery("items[*].id")
s := streamjson.NewScanner(file, q) // file 是任何 io.Reader
defer s.Close()
for {
    m, err := s.Next(ctx)
    if err == io.EOF {
        break
    }
    if err != nil {
        log.Fatal(err) // 取消或超时错误
    }
    fmt.Printf("doc=%d offset=%d path=%s raw=%s\n",
        m.DocIndex, m.Offset, m.Path, m.Raw)
}
// 扫描结束后检查每份文档的错误与未命中原因
for _, de := range s.DocErrors() { ... }
for _, miss := range s.Misses() { ... }
```

## 查询语义

路径表达式由若干段组成：

| 语法        | 含义                                   |
|-------------|----------------------------------------|
| `a.b.c`     | 嵌套对象字段                           |
| `a[0]`      | 数组下标（从 0 开始）                  |
| `a.*` / `a[*]` | 通配：对象的任意字段 / 数组的任意元素 |
| `*`         | 根对象的全部字段（或根数组的全部元素） |
| 空串 `""`   | 匹配整份文档                           |

- 匹配按输入顺序逐条产出，相同输入的产出顺序逐条一致（确定性）。
- 每个 `Match` 携带：`DocIndex`（来自第几份拼接文档）、`Offset`（匹配值在
  输入中的绝对字节偏移）、`Path`（通配符已解析为实际路径）、`Raw`（匹配值
  的原始 JSON 字节）。
- 缺失字段不会被当成空值参与判定。文档未产生匹配时，`Misses()` 给出可区分
  的原因：
  - `MissMissingField`：对象缺少查询要求的字段；
  - `MissTypeMismatch`：路径段作用在了不兼容的类型上（如对数组取字段、
    对标量继续下钻、对对象取下标）；
  - `MissIndexOutOfRange`：数组下标越界（含对空数组使用通配符）。
  `Miss.Path` 是匹配失败处的具体路径前缀。

## 多文档拼接与错误恢复

输入可以是多份 JSON 文档顺序拼接（空白或换行分隔），每份独立解析：

- 某份文档出错不影响其余文档；错误记录在 `DocErrors()` 中，扫描自动从
  下一份文档继续。
- 错误类型可区分：`streamjson.IsTruncated(err)` 为真表示输入被截断
  （不完整），否则为内容非法（语法错误）。`*streamjson.Error` 携带
  `DocIndex` 与绝对 `Offset`。
- 错误恢复按行对齐：出错后解析器跳到下一行边界重新解析下一份文档。
  因此需要错误隔离时，请以换行分隔各文档（如 NDJSON）。
- 截断文档中已完整读出的匹配仍会正常产出，随后该文档被报告为截断。

## 取消、超时与背压

- `Scanner` 是拉取（pull）模型：只有调用 `Next` 时解析才推进，消费端慢
  时读取端自然停顿，不会无界堆积内存，也没有后台读取任务。
- `Next(ctx)` 在每次读取间检查 `ctx`：`context.WithCancel` /
  `context.WithTimeout` 都会让 `Next` 及时返回 `ctx.Err()`。
  若底层 `Reader` 本身阻塞在单次 `Read` 中，请让该 `Reader` 响应
  `ctx`（在 `ctx.Done()` 后返回错误）以获得即时取消。
- 消费方可随时停止调用 `Next` 并调用 `Close` 提前结束，不会停死或泄漏。

## 并发

`Scanner` 实例之间完全独立、无共享状态：每个 goroutine 使用自己的
`Scanner` 即可并发解析，慢任务不会阻塞其他任务。

`Scanner.Stream(ctx, bufSize)` 是可选的通道封装：它在单个后台 goroutine
中驱动 `Next`，向有界通道投递结果。提前结束消费时必须取消 `ctx`
（或 `Close`），goroutine 随即退出，不会泄漏。

## 内存与性能

- 解析器内存占用上界 = 嵌套深度 + 最长单个 token + 32KB 读缓冲。
- 匹配值的捕获缓冲只保存当前匹配子树，随匹配完成即释放。
- 相同输入下结果顺序确定，可稳定复现。

## 测试

```sh
go test -race ./...
```

覆盖：超大输入（20 万条记录）、多文档拼接、嵌套与通配查询、截断与非法
内容、取消与超时、并发扫描、背压、提前关闭与 goroutine 泄漏检查。
