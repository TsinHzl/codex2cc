package proxy

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/codex2cc/database"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// imageUsageLogInfo 及其辅助函数从源 images.go 抽离（codex2cc 不做生图渠道，
// 仅保留 /v1/messages 链路上的用量统计结构）。

type imageCallResult struct {
	// Result 承载上游返回的 base64 图像数据，可能达数 MB。本结构体只用于
	// 抽取用量元数据，任何日志、错误信息或序列化路径都不得打印该字段——
	// 它既不参与计量，也不该出现在任何持久化或输出通道里。
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	ByteSize      int
	Width         int
	Height        int
	Background    string
	Quality       string
	Model         string
}

// clampNonNegativeInt 把上游 JSON 中的整数字段收敛到 [0, MaxInt32]。
// 上游可能回负数或超大值，直接 int() 截断在 32 位平台会产生负数或回绕，
// 污染用量统计（字节数/宽高为负会让聚合结果失真）。
func clampNonNegativeInt(value int64) int {
	if value <= 0 || value > math.MaxInt32 {
		return 0
	}
	return int(value)
}

func ImagesDefaultMainModel() string {
	if value := strings.TrimSpace(os.Getenv(imagesMainModelEnv)); value != "" {
		return value
	}
	return defaultImagesMainModel
}

// NormalizeImagesMainModel 校验文本驱动名称；空值表示使用部署默认值。
// 允许尚未同步到模型目录的名称，以便使用中转或新发布的文本模型。

func NormalizeImagesMainModel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 128 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", fmt.Errorf("codex_images_main_model 必须是不超过 128 字节且不含空白的模型名称")
	}
	if isImageOnlyModel(value) {
		return "", fmt.Errorf("codex_images_main_model 必须是文本模型，不能使用图像模型")
	}
	return value, nil
}

// imagesMainModel 在每次构造生图请求时读取配置，后台保存后立即生效。
// 优先级：系统设置 > 环境变量 > 内置默认值。

func imagesMainModel() string {
	if value := CurrentRuntimeSettings().CodexImagesMainModel; value != "" {
		return value
	}
	return ImagesDefaultMainModel()
}

// imagesMainModelCandidates 返回驱动主模型的完整候选序列(首选 + 回退),去重且
// 排除生图模型本身。

type imageUsageLogInfo struct {
	Count  int
	Width  int
	Height int
	Bytes  int
	Format string
	Size   string
}

func imageUsageLogInfoFromImage(image imageCallResult) imageUsageLogInfo {
	populateImageStats(&image)
	info := imageUsageLogInfo{
		Count:  1,
		Width:  image.Width,
		Height: image.Height,
		Bytes:  image.ByteSize,
		Format: strings.TrimSpace(image.OutputFormat),
		Size:   strings.TrimSpace(image.Size),
	}
	return info
}

func mergeImageUsageLogInfo(current imageUsageLogInfo, next imageUsageLogInfo) imageUsageLogInfo {
	if next.Count <= 0 {
		return current
	}
	if current.Count <= 0 {
		return next
	}
	current.Count += next.Count
	if current.Width == 0 {
		current.Width = next.Width
	}
	if current.Height == 0 {
		current.Height = next.Height
	}
	if current.Bytes == 0 {
		current.Bytes = next.Bytes
	}
	if current.Format == "" {
		current.Format = next.Format
	}
	if current.Size == "" {
		current.Size = next.Size
	}
	return current
}

func imageUsageLogInfoFromImages(images []imageCallResult) imageUsageLogInfo {
	var info imageUsageLogInfo
	for _, image := range images {
		info = mergeImageUsageLogInfo(info, imageUsageLogInfoFromImage(image))
	}
	return info
}

func imageUsageLogInfoFromResponseJSON(responseJSON []byte) imageUsageLogInfo {
	var info imageUsageLogInfo
	output := gjson.GetBytes(responseJSON, "output")
	if !output.IsArray() {
		return info
	}
	for _, item := range output.Array() {
		if item.Get("type").String() != "image_generation_call" || strings.TrimSpace(item.Get("result").String()) == "" {
			continue
		}
		image := imageCallResult{
			Result:       strings.TrimSpace(item.Get("result").String()),
			OutputFormat: strings.TrimSpace(item.Get("output_format").String()),
			Size:         strings.TrimSpace(item.Get("size").String()),
			ByteSize:     clampNonNegativeInt(item.Get("bytes").Int()),
			Width:        clampNonNegativeInt(item.Get("width").Int()),
			Height:       clampNonNegativeInt(item.Get("height").Int()),
		}
		info = mergeImageUsageLogInfo(info, imageUsageLogInfoFromImage(image))
	}
	return info
}

func applyImageUsageLogInfo(input *database.UsageLogInput, info imageUsageLogInfo) {
	if input == nil || info.Count <= 0 {
		return
	}
	input.ImageCount = info.Count
	input.ImageWidth = info.Width
	input.ImageHeight = info.Height
	input.ImageBytes = info.Bytes
	input.ImageFormat = info.Format
	input.ImageSize = info.Size
}

func isImageOnlyModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

// IsGPTImageModel 判断模型名是否属于 Codex 生图模型族(gpt-image-*),含 -2k/-4k
// 档位别名与带日期的快照名。供 admin 生图台等外部包复用同一准入判定。

func normalizeImageToolModelForPrompt(model string, prompt string) (string, string) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultImagesToolModel
	}
	base, tier := splitImageModelSizeAlias(model)
	if strings.EqualFold(base, defaultImagesToolModel) {
		base = defaultImagesToolModel
	}
	if !isGPTImage2FamilyModel(base) {
		// gpt-image-1.5 等更早型号或未知名字:原样透传,尺寸交给上游默认。
		return model, ""
	}
	switch tier {
	case imageModel2KSuffix:
		return base, inferDefaultImageSize(prompt, imageDefaultSizeSet{
			defaultSize:   defaultImages2KSize,
			squareSize:    defaultImages2KSize,
			landscapeSize: defaultImages2KLandscapeSize,
			portraitSize:  defaultImages2KPortraitSize,
		})
	case imageModel4KSuffix:
		return base, inferDefaultImageSize(prompt, imageDefaultSizeSet{
			defaultSize:   defaultImages4KSize,
			squareSize:    defaultImages4KSquareSize,
			landscapeSize: defaultImages4KLandscapeSize,
			portraitSize:  defaultImages4KPortraitSize,
		})
	default:
		return base, inferDefaultImageSize(prompt, imageDefaultSizeSet{
			defaultSize:   defaultImages1KSize,
			squareSize:    defaultImages1KSize,
			landscapeSize: defaultImages1KLandscapeSize,
			portraitSize:  defaultImages1KPortraitSize,
		})
	}
}

func validateResponsesImageGenerationSizes(body []byte) error {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}
	for i, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) != "image_generation" {
			continue
		}
		if !shouldValidateGPTImage2Size(tool.Get("model").String()) {
			continue
		}
		size := tool.Get("size")
		if !size.Exists() || size.Type == gjson.Null {
			continue
		}
		if size.Type != gjson.String {
			return fmt.Errorf("image_generation tool %d size must be a string like 1024x1024 or auto", i)
		}
		if err := validateGPTImage2Size(size.String()); err != nil {
			return fmt.Errorf("image_generation tool %d: %w", i, err)
		}
	}
	return nil
}

func responsesBodyHasImageGenerationTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		for _, tool := range tools.Array() {
			if strings.TrimSpace(tool.Get("type").String()) == "image_generation" {
				return true
			}
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		return strings.EqualFold(strings.TrimSpace(choice.String()), "image_generation")
	}
	return strings.EqualFold(strings.TrimSpace(choice.Get("type").String()), "image_generation")
}

func responsesBodyRequestsImageGeneration(body []byte) bool {
	if isImageOnlyModel(gjson.GetBytes(body, "model").String()) {
		return true
	}
	choice := gjson.GetBytes(body, "tool_choice")
	if choice.Type == gjson.String && strings.EqualFold(strings.TrimSpace(choice.String()), "image_generation") {
		return true
	}
	if choice.Exists() && strings.EqualFold(strings.TrimSpace(choice.Get("type").String()), "image_generation") {
		return true
	}
	for _, key := range responsesImageGenerationOptionFields {
		value := gjson.GetBytes(body, key)
		if value.Exists() && value.Type != gjson.Null {
			return true
		}
	}
	return false
}

// rawResponsesBodyShouldForceHTTPForImageGeneration 判断请求是否"真的要生图"，
// 从而必须改走 HTTP 上游——WebSocket 上游传输大体积图片数据会卡死（issue #220）。
//
// 只在"真实生图意图"时强制 HTTP：image-only 模型、tool_choice=image_generation、
// 顶层图片选项字段（responsesBodyRequestsImageGeneration），或 prompt 中的自然语言
// 生图意图（issue #288）。它刻意**不**因 tools[] 里单纯存在 image_generation 工具而
// 触发：客户端把生图工具无差别注入到每个请求时（issue #304），否则会把普通请求也全部
// 打到 HTTP、丢掉 WebSocket。这类"注入但未使用"的工具改由 WS 路径上的
// stripResponsesImageGenerationTool 剥离。
//
// /v1/responses 路径须传入下游 raw body（PrepareResponsesBody 注入默认工具之前）；
// chat 路径传入翻译后的 Codex body（TranslateRequest 不会自动注入图片工具）。两者都
// 不应含自动注入的 tool_choice=image_generation，否则普通请求会被误判。

func promptTextRequestsImageGeneration(text string) bool {
	normalized := normalizeImageIntentText(text)
	if normalized == "" {
		return false
	}
	if containsAnyPhrase(normalized, imageIntentFalsePositivePhrases) {
		return false
	}
	return containsAnyPhrase(normalized, imageIntentPositivePhrases)
}

func stripResponsesImageGenerationTool(body []byte) []byte {
	return stripResponsesImageGenerationCapabilities(body)
}

// stripImageGenerationToolsFromArray 从工具数组里剔除图片能力声明：扁平的
// {"type":"image_generation"} 与 namespace 形式 {"type":"namespace","name":"image_gen"}。
// 返回保留下来的工具及是否发生过移除。

func stripResponsesImageGenerationCapabilities(body []byte) []byte {
	// 1. 顶层 tools[]
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		if kept, removed := stripImageGenerationToolsFromArray(tools.Array()); removed {
			if len(kept) == 0 {
				body, _ = sjson.DeleteBytes(body, "tools")
			} else {
				body, _ = sjson.SetBytes(body, "tools", kept)
			}
		}
	}

	// 2. Responses Lite: only inspect matching carriers. Ordinary conversation
	// items stay opaque, including when a different carrier must be rewritten.
	body = stripResponsesInputImageTools(body)

	// 3. tool_choice：仅删显式指向图片工具的选择
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() {
		var target string
		if choice.Type == gjson.String {
			target = strings.TrimSpace(choice.String())
		} else {
			target = strings.TrimSpace(choice.Get("type").String())
			if target == "namespace" {
				target = strings.TrimSpace(choice.Get("name").String())
			}
		}
		if strings.EqualFold(target, "image_generation") || strings.EqualFold(target, "image_gen") {
			body, _ = sjson.DeleteBytes(body, "tool_choice")
		}
	}

	// 4. 桥接 instructions（网关注入的引导文案）
	if instructions := gjson.GetBytes(body, "instructions").String(); strings.Contains(instructions, codexImageGenerationBridgeMarker) {
		cleaned := strings.ReplaceAll(instructions, "\n\n"+codexImageGenerationBridgeText, "")
		cleaned = strings.ReplaceAll(cleaned, codexImageGenerationBridgeText, "")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			body, _ = sjson.DeleteBytes(body, "instructions")
		} else {
			body, _ = sjson.SetBytes(body, "instructions", cleaned)
		}
	}
	return body
}

func mediaOnlyModelEndpoints(model string) string {
	_ = model
	return "/v1/images/generations and /v1/images/edits"
}

func extractImageFromOutputItemDone(payload []byte, fallbackModel string) (imageCallResult, bool) {
	if gjson.GetBytes(payload, "type").String() != "response.output_item.done" {
		return imageCallResult{}, false
	}
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || item.Get("type").String() != "image_generation_call" {
		return imageCallResult{}, false
	}
	result := strings.TrimSpace(item.Get("result").String())
	if result == "" {
		return imageCallResult{}, false
	}
	image := imageCallResult{
		Result:        result,
		RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
		OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
		Size:          strings.TrimSpace(item.Get("size").String()),
		ByteSize:      clampNonNegativeInt(item.Get("bytes").Int()),
		Width:         clampNonNegativeInt(item.Get("width").Int()),
		Height:        clampNonNegativeInt(item.Get("height").Int()),
		Background:    strings.TrimSpace(item.Get("background").String()),
		Quality:       strings.TrimSpace(item.Get("quality").String()),
		Model:         fallbackModel,
	}
	populateImageStats(&image)
	return image, true
}


// --- 从源 images.go 追加的尺寸/工具模型辅助（保持原实现，供用量统计与告警文案复用） ---
func populateImageStats(image *imageCallResult) bool {
	if image == nil || strings.TrimSpace(image.Result) == "" {
		return false
	}
	// 字段已由上游 JSON 齐全提供时无需解码 base64（可能达数 MB），避免无谓开销。
	if image.ByteSize > 0 && image.Width > 0 && image.Height > 0 {
		return false
	}
	stats, ok := imageStatsFromBase64(image.Result)
	if !ok {
		return false
	}
	changed := false
	if stats.ByteSize > 0 && image.ByteSize == 0 {
		image.ByteSize = stats.ByteSize
		changed = true
	}
	if stats.Width > 0 && image.Width == 0 {
		image.Width = stats.Width
		changed = true
	}
	if stats.Height > 0 && image.Height == 0 {
		image.Height = stats.Height
		changed = true
	}
	return changed
}

func splitImageModelSizeAlias(model string) (string, string) {
	model = strings.TrimSpace(model)
	lower := strings.ToLower(model)
	for _, suffix := range []string{imageModel2KSuffix, imageModel4KSuffix} {
		if strings.HasSuffix(lower, suffix) && len(lower) > len(suffix) {
			base := model[:len(model)-len(suffix)]
			if isImageOnlyModel(base) {
				return base, suffix
			}
		}
	}
	return model, ""
}

// isGPTImage2FamilyModel 判断是否 gpt-image-2 世代(含 2.5 flare/sunburst 及其快照名):
// 这一族共用 1K/2K/4K 尺寸档位与提示词方向推断;更早或未知型号不补默认尺寸。

func isGPTImage2FamilyModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-2")
}

type imageDefaultSizeSet struct {
	defaultSize   string
	squareSize    string
	landscapeSize string
	portraitSize  string
}


func inferDefaultImageSize(prompt string, sizes imageDefaultSizeSet) string {
	switch inferImageAspectFromPrompt(prompt) {
	case "square":
		if sizes.squareSize != "" {
			return sizes.squareSize
		}
	case "landscape":
		if sizes.landscapeSize != "" {
			return sizes.landscapeSize
		}
	case "portrait":
		if sizes.portraitSize != "" {
			return sizes.portraitSize
		}
	}
	return sizes.defaultSize
}

func shouldValidateGPTImage2Size(model string) bool {
	toolModel, _ := normalizeImageToolModel(model)
	return isGPTImage2FamilyModel(toolModel)
}

func validateGPTImage2Size(size string) error {
	raw := strings.TrimSpace(size)
	if raw == "" || strings.EqualFold(raw, "auto") {
		return nil
	}

	parts := strings.Split(strings.ToLower(raw), "x")
	if len(parts) != 2 {
		return fmt.Errorf("image size %q must use WIDTHxHEIGHT format or auto", raw)
	}
	width, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || width <= 0 {
		return fmt.Errorf("image size %q has invalid width", raw)
	}
	height, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || height <= 0 {
		return fmt.Errorf("image size %q has invalid height", raw)
	}
	if width%16 != 0 || height%16 != 0 {
		return fmt.Errorf("image size %q is invalid: width and height must be multiples of 16", raw)
	}
	pixels := int64(width) * int64(height)
	if pixels > maxGPTImage2Pixels {
		return fmt.Errorf("image size %q is invalid: total pixels %d exceeds max %d", raw, pixels, maxGPTImage2Pixels)
	}
	longSide, shortSide := width, height
	if height > width {
		longSide, shortSide = height, width
	}
	if int64(longSide) > int64(shortSide)*3 {
		return fmt.Errorf("image size %q is invalid: aspect ratio must not exceed 3:1", raw)
	}
	return nil
}
const (
	// defaultImagesMainModel 是生图链路里驱动 image_generation 工具调用的主模型
	// (图像本身由 tools[0].model 决定,主模型只负责发起工具调用,token 开销极小)。
	// 2026-09 起 ChatGPT 账号的 Codex manifest 已不含 gpt-5.4-mini,上游对它直接回
	// 400 "The 'gpt-5.4-mini' model is not supported when using Codex with a ChatGPT
	// account",整条生图链路随之全断;free/plus/pro 三档 manifest 均含 gpt-5.6-luna,
	// 故改用它。系统生图设置或 CODEX_IMAGES_MAIN_MODEL 可覆盖;上游再次下线时,
	// imagesMainModelFallbacks 会在同一账号上按序换驱动重试,不会把 400 记到生图模型头上。
	defaultImagesMainModel = "gpt-5.6-luna"
	defaultImagesToolModel = "gpt-image-2"

	imageModel2KAlias = "gpt-image-2-2k"
	imageModel4KAlias = "gpt-image-2-4k"

	// imageModel2KSuffix / imageModel4KSuffix 是分辨率档位别名后缀:任意
	// gpt-image-* 模型都可以带 -2k / -4k(如 gpt-image-2-4k),网关剥掉后缀
	// 作为 tools[0].model 发上游,并按档位补默认尺寸与超分计划。
	imageModel2KSuffix = "-2k"
	imageModel4KSuffix = "-4k"

	imagesMainModelEnv = "CODEX_IMAGES_MAIN_MODEL"

	defaultImages1KSize = "1024x1024"
	defaultImages2KSize = "2048x2048"
	defaultImages4KSize = "3840x2160"

	defaultImages1KLandscapeSize = "1536x864"
	defaultImages1KPortraitSize  = "864x1536"
	defaultImages2KLandscapeSize = "2560x1440"
	defaultImages2KPortraitSize  = "1440x2560"
	defaultImages4KLandscapeSize = defaultImages4KSize
	defaultImages4KPortraitSize  = "2160x3840"
	defaultImages4KSquareSize    = "2880x2880"

	maxGPTImage2Pixels = 8294400

	// maxImageAttempts caps ordinary finite image retries. A failure selected by
	// continuous retry bypasses it until the client disconnects.
	maxImageAttempts = 5

	// MaxImageEditInputCount caps the number of input images for edit requests.
	// 与官方 Images API 对 gpt-image 系列的上限一致（16 张）；上游 responses
	// 通道已实测可接受 16 张 input_image（issue #275）。
	MaxImageEditInputCount = 16

	imageStreamConnectedComment = ": connected\n\n"

	// imageCloudURLTTL 控制 response_format=url 时返回的预签名云直链有效期。
	imageCloudURLTTL = time.Hour

	imageOAuthUnavailableCooldown = 30 * time.Minute
	imageModelTextMaxBytes        = 600
)

// imagesMainModelFallbacks 是驱动主模型被上游按"不支持"拒绝时的候选序列,
// 按 free/plus/pro 三档 manifest 的交集从便宜到贵排列。
type imageOutputStats struct {
	ByteSize int
	Width    int
	Height   int
}


func decodeImageBase64(raw string) ([]byte, bool) {
	encoded := strings.TrimSpace(raw)
	if encoded == "" {
		return nil, false
	}
	// Guard against extremely large inputs (100 MB limit).
	const maxDecodeInputLen = 100 * 1024 * 1024
	if len(encoded) > maxDecodeInputLen {
		return nil, false
	}
	if strings.HasPrefix(strings.ToLower(encoded), "data:") {
		if comma := strings.Index(encoded, ","); comma >= 0 {
			encoded = encoded[comma+1:]
		}
	}
	if strings.ContainsAny(encoded, " \t\r\n") {
		encoded = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(encoded)
	}
	// Prefer URL-safe encodings when the input contains '-' or '_'
	// (characters that only appear in URL-safe base64).
	preferURLSafe := strings.ContainsAny(encoded, "-_")
	encodings := [4]*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	if preferURLSafe {
		encodings = [4]*base64.Encoding{
			base64.URLEncoding,
			base64.RawURLEncoding,
			base64.StdEncoding,
			base64.RawStdEncoding,
		}
	}
	for _, encoding := range encodings {
		data, err := encoding.DecodeString(encoded)
		if err == nil {
			return data, true
		}
	}
	return nil, false
}


func decodeWebPDimensions(data []byte) (int, int, bool) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	for offset := 12; offset+8 <= len(data); {
		chunkType := string(data[offset : offset+4])
		// RIFF chunk size 是无符号 32 位；必须先转 uint32 再扩宽到 int64，
		// 否则高位字节在 32 位 int 上会溢出成负数，`chunkSize < 0` 判定失效，
		// payloadEnd 回绕成小正数并通过越界检查，后续切片会 panic。
		chunkSize := int64(uint32(data[offset+4]) | uint32(data[offset+5])<<8 |
			uint32(data[offset+6])<<16 | uint32(data[offset+7])<<24)
		payloadStart := offset + 8
		if chunkSize > int64(len(data)-payloadStart) {
			return 0, 0, false
		}
		payloadEnd := payloadStart + int(chunkSize)
		payload := data[payloadStart:payloadEnd]
		switch chunkType {
		case "VP8X":
			if len(payload) >= 10 {
				width := 1 + int(payload[4]) + int(payload[5])<<8 + int(payload[6])<<16
				height := 1 + int(payload[7]) + int(payload[8])<<8 + int(payload[9])<<16
				return width, height, true
			}
		case "VP8 ":
			if len(payload) >= 10 && payload[3] == 0x9d && payload[4] == 0x01 && payload[5] == 0x2a {
				width := (int(payload[6]) | int(payload[7])<<8) & 0x3fff
				height := (int(payload[8]) | int(payload[9])<<8) & 0x3fff
				return width, height, true
			}
		case "VP8L":
			if len(payload) >= 5 && payload[0] == 0x2f {
				bits := int(payload[1]) | int(payload[2])<<8 | int(payload[3])<<16 | int(payload[4])<<24
				width := 1 + (bits & 0x3fff)
				height := 1 + ((bits >> 14) & 0x3fff)
				return width, height, true
			}
		}
		offset = payloadEnd
		if chunkSize%2 == 1 {
			offset++
		}
	}
	return 0, 0, false
}

