# Limen

Go 图片压缩库。默认优先尝试无损 WebP，未能缩小时回退到原格式压缩。

HEIC 转 WebP 适用于受支持的 8 位 SDR 静态图像。HDR、广色域和序列保留为 HEIC。

```go
import "github.com/alphaidea/limen"

// format 为实际输出格式。
output, format, err := limen.Compress(input)
```

可按需追加选项，例如 `limen.Compress(input, limen.KeepFormat())`。

- `BrowserImage()` 面向现代主流浏览器。对于受支持的 HEIC、HEIF、TIFF 和 JPEG XL，优先采用比原文件或 PNG 更小的无损 WebP，否则回退到经像素验证的 PNG。输出可能比输入大。无法保留显示效果或选项冲突时返回 `ErrBrowserImage`。
- `KeepFormat()` 仅按原格式压缩。`PreserveMetadata()` 同时保留原格式和元数据。
- `ExperimentalNearLosslessPNG()` 允许轻微色差。`Zopfli()` 为 PNG、APNG 增加一轮较慢的无损压缩。
- `StandaloneSVG()` 精简独立静态 SVG 的数值和颜色写法。
- `JPEGQualityCheck`、`PNGQualityCheck`、`WebPQualityCheck`、`AVIFQualityCheck`、`HEICQualityCheck`、`JPEGXLQualityCheck` 接受回调 `func(original, candidate []byte) bool`，返回 `true` 表示候选图像通过质量检查。
