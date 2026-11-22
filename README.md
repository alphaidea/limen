# Limen

Go 图片压缩库。默认优先尝试无损 WebP，未能缩小时回退到原格式压缩。

```go
import "github.com/alphaidea/limen"

// format 为实际输出格式。
output, format, err := limen.Compress(input)
```

可按需追加选项，例如 `limen.Compress(input, limen.KeepFormat())`。

- `KeepFormat()` 仅按原格式压缩。`PreserveMetadata()` 同时保留原格式和元数据。
- `ExperimentalNearLosslessPNG()` 允许轻微色差。`Zopfli()` 为 PNG、APNG 增加一轮较慢的无损压缩。
- `StandaloneSVG()` 精简独立静态 SVG 的数值和颜色写法。
- `JPEGQualityCheck`、`PNGQualityCheck`、`WebPQualityCheck`、`AVIFQualityCheck`、`HEICQualityCheck`、`JPEGXLQualityCheck` 接受回调 `func(original, candidate []byte) bool`，返回 `true` 表示候选图像通过质量检查。
