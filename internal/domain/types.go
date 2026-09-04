package domain

import "time"

// Platform 表示短视频内容要适配的平台。
type Platform string

const (
	PlatformXiaohongshu    Platform = "xiaohongshu"
	PlatformDouyin         Platform = "douyin"
	PlatformWechatChannels Platform = "wechat_channels"
)

// OfferingKind 区分实体商品、服务和可交付的问题解决能力。
type OfferingKind string

const (
	OfferingKindProduct    OfferingKind = "product"
	OfferingKindService    OfferingKind = "service"
	OfferingKindCapability OfferingKind = "capability"
)

// Template 描述一个可执行的平台内容模板。
type Template struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Platform    Platform `json:"platform"`
	Description string   `json:"description"`
	Stages      []string `json:"stages"`
}

// CreateProjectRequest 是创建单个视频项目的请求。
type CreateProjectRequest struct {
	Name            string        `json:"name"`
	Platform        Platform      `json:"platform"`
	Template        string        `json:"template"`
	DurationSeconds int           `json:"duration_seconds"`
	Offering        OfferingInput `json:"offering"`
	// Product 保留用于兼容现有客户端。
	Product ProductInput `json:"product"`
}

// OfferingInput 承载商品、服务或能力及其素材和目标人群等原始输入。
type OfferingInput struct {
	Kind OfferingKind `json:"kind"`
	Name string       `json:"name"`
	// Title、SellingPoints 和 PainPoints 是兼容旧商品输入的别名。
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Link           string   `json:"link"`
	Price          string   `json:"price"`
	Category       string   `json:"category"`
	ValuePoints    []string `json:"value_points"`
	SellingPoints  []string `json:"selling_points"`
	Problems       []string `json:"problems"`
	PainPoints     []string `json:"pain_points"`
	TargetAudience string   `json:"target_audience"`
	UsageScenarios []string `json:"usage_scenarios"`
	Assets         []string `json:"assets"`
	DeliveryMode   string   `json:"delivery_mode"`
	ProofPoints    []string `json:"proof_points"`
	CallToAction   string   `json:"call_to_action"`
}

// ProductInput 是 Offering 迁移期间保留的源码兼容别名。
type ProductInput = OfferingInput

// VideoProject 是单个商品/选题的视频生产项目。
type VideoProject struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Platform        Platform      `json:"platform"`
	Template        string        `json:"template"`
	DurationSeconds int           `json:"duration_seconds"`
	Status          string        `json:"status"`
	Offering        OfferingInput `json:"offering"`
	// Product 与 Offering 同步，供尚未迁移的客户端读取。
	Product      ProductInput        `json:"product"`
	Brief        *PublishingBrief    `json:"brief,omitempty"`
	Variants     []Variant           `json:"variants,omitempty"`
	Render       *RenderResult       `json:"render,omitempty"`
	Publications []PublicationResult `json:"publications,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// ProductAnalysis 是 workflow 对商品输入的结构化分析。
type ProductAnalysis struct {
	CoreSellingPoint     string   `json:"core_selling_point"`
	FunctionalPoints     []string `json:"functional_points"`
	EmotionalPoints      []string `json:"emotional_points"`
	ScenarioPoints       []string `json:"scenario_points"`
	AudiencePainPoints   []string `json:"audience_pain_points"`
	TrustBuilders        []string `json:"trust_builders"`
	ForbiddenExpressions []string `json:"forbidden_expressions"`
	ContentOpportunities []string `json:"content_opportunities"`
}

// ContentAngle 表示一个内容切入角度和对应 hook。
type ContentAngle struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Hook        string `json:"hook"`
	Description string `json:"description"`
}

// Script 是一个可拍摄的短视频脚本版本。
type Script struct {
	ID            string `json:"id"`
	Style         string `json:"style"`
	Title         string `json:"title"`
	Hook          string `json:"hook"`
	Body          string `json:"body"`
	CallToAction  string `json:"call_to_action"`
	FullScript    string `json:"full_script"`
	VoiceoverText string `json:"voiceover_text"`
	SubtitleText  string `json:"subtitle_text"`
	EstimatedS    int    `json:"estimated_seconds"`
}

// Shot 表示分镜表中的一条镜头建议。
type Shot struct {
	Index       int    `json:"index"`
	StartSecond int    `json:"start_second"`
	EndSecond   int    `json:"end_second"`
	Visual      string `json:"visual"`
	Voiceover   string `json:"voiceover"`
	Subtitle    string `json:"subtitle"`
	AssetType   string `json:"asset_type"`
	AssetHint   string `json:"asset_hint"`
	Transition  string `json:"transition"`
	Purpose     string `json:"purpose"`
}

// Storyboard 是某个脚本对应的分镜表。
type Storyboard struct {
	ScriptID string `json:"script_id"`
	Shots    []Shot `json:"shots"`
}

// MaterialPlan 描述可用素材、缺失素材和补拍建议。
type MaterialPlan struct {
	AvailableAssets []string `json:"available_assets"`
	NeededAssets    []string `json:"needed_assets"`
	ShootList       []string `json:"shoot_list"`
	AIAssetIdeas    []string `json:"ai_asset_ideas"`
}

// PublishingBrief 是运营人员审核发布包的核心结构化产物。
type PublishingBrief struct {
	ID              string          `json:"id"`
	ProjectID       string          `json:"project_id"`
	GeneratedAt     time.Time       `json:"generated_at"`
	ProductAnalysis ProductAnalysis `json:"product_analysis"`
	Angles          []ContentAngle  `json:"angles"`
	TitleOptions    []string        `json:"title_options"`
	CoverTexts      []string        `json:"cover_texts"`
	Scripts         []Script        `json:"scripts"`
	Storyboard      Storyboard      `json:"storyboard"`
	MaterialPlan    MaterialPlan    `json:"material_plan"`
	VoiceoverText   string          `json:"voiceover_text"`
	SubtitleText    string          `json:"subtitle_text"`
	Caption         string          `json:"caption"`
	Tags            []string        `json:"tags"`
	QA              QAReport        `json:"qa"`
}

// Variant 是一个 A/B 测试版本，覆盖标题、封面、hook、CTA 等维度。
type Variant struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"project_id"`
	Title          string        `json:"title"`
	CoverText      string        `json:"cover_text"`
	Hook           string        `json:"hook"`
	ScriptStyle    string        `json:"script_style"`
	CallToAction   string        `json:"call_to_action"`
	Caption        string        `json:"caption"`
	Differentiator string        `json:"differentiator"`
	Metrics        VariantMetric `json:"metrics"`
	CreatedAt      time.Time     `json:"created_at"`
}

// VariantMetric 记录人工回填的单个变体表现数据。
type VariantMetric struct {
	Views          int     `json:"views"`
	Likes          int     `json:"likes"`
	Saves          int     `json:"saves"`
	Comments       int     `json:"comments"`
	CompletionRate float64 `json:"completion_rate"`
	Note           string  `json:"note"`
	UpdatedAt      string  `json:"updated_at,omitempty"`
}

// QAReport 是平台适配和内容风险检查结果。
type QAReport struct {
	Passed bool     `json:"passed"`
	Score  int      `json:"score"`
	Issues []string `json:"issues"`
	Advice []string `json:"advice"`
}

// ExportPackage 是单项目导出接口的内存响应结构。
type ExportPackage struct {
	ProjectID string `json:"project_id"`
	Format    string `json:"format"`
	Markdown  string `json:"markdown,omitempty"`
	JSON      any    `json:"json,omitempty"`
}

// BatchStatus 表示批量任务或单个批量条目的执行状态。
type BatchStatus string

const (
	BatchStatusPending             BatchStatus = "pending"
	BatchStatusRunning             BatchStatus = "running"
	BatchStatusCompleted           BatchStatus = "completed"
	BatchStatusCompletedWithErrors BatchStatus = "completed_with_errors"
	BatchStatusFailed              BatchStatus = "failed"
)

// BatchRunRequest 是同步运行批量视频工厂的请求。
type BatchRunRequest struct {
	Name            string          `json:"name"`
	Platform        Platform        `json:"platform"`
	Template        string          `json:"template"`
	DurationSeconds int             `json:"duration_seconds"`
	Offerings       []OfferingInput `json:"offerings"`
	// Products 保留用于兼容现有客户端。
	Products      []ProductInput           `json:"products"`
	VariantCount  int                      `json:"variant_count"`
	ExportFormats []string                 `json:"export_formats"`
	Render        bool                     `json:"render"`
	RenderRequest RenderOpenMontageRequest `json:"render_request,omitempty"`
}

// BatchItem 记录一个商品在 batch 中对应的项目、导出路径和错误。
type BatchItem struct {
	Index                  int         `json:"index"`
	OfferingName           string      `json:"offering_name"`
	ProductTitle           string      `json:"product_title"`
	ProjectID              string      `json:"project_id,omitempty"`
	Status                 BatchStatus `json:"status"`
	Error                  string      `json:"error,omitempty"`
	PublishPackagePath     string      `json:"publish_package_path,omitempty"`
	PublishPackageJSONPath string      `json:"publish_package_json_path,omitempty"`
	RenderVideoPath        string      `json:"render_video_path,omitempty"`
}

// BatchJob 是一次批量生产任务的可追踪记录。
type BatchJob struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Status         BatchStatus `json:"status"`
	Platform       Platform    `json:"platform"`
	Template       string      `json:"template"`
	VariantCount   int         `json:"variant_count"`
	Render         bool        `json:"render"`
	Items          []BatchItem `json:"items"`
	ReportPath     string      `json:"report_path,omitempty"`
	ReportJSONPath string      `json:"report_json_path,omitempty"`
	ExportDir      string      `json:"export_dir,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// BatchReport 是写入 batch_report.json 的人工可读报告数据。
type BatchReport struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Status         BatchStatus       `json:"status"`
	Platform       Platform          `json:"platform"`
	Template       string            `json:"template"`
	OfferingCount  int               `json:"offering_count"`
	ProductCount   int               `json:"product_count"`
	VariantCount   int               `json:"variant_count"`
	Render         bool              `json:"render"`
	Items          []BatchReportItem `json:"items"`
	NextSteps      []string          `json:"next_steps"`
	ExportDir      string            `json:"export_dir"`
	ReportPath     string            `json:"report_path"`
	ReportJSONPath string            `json:"report_json_path"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// BatchReportItem 是 batch report 中面向运营审核的一行商品摘要。
type BatchReportItem struct {
	Index                  int         `json:"index"`
	OfferingName           string      `json:"offering_name"`
	ProductTitle           string      `json:"product_title"`
	ProjectID              string      `json:"project_id,omitempty"`
	Status                 BatchStatus `json:"status"`
	Error                  string      `json:"error,omitempty"`
	PublishPackagePath     string      `json:"publish_package_path,omitempty"`
	PublishPackageJSONPath string      `json:"publish_package_json_path,omitempty"`
	RenderVideoPath        string      `json:"render_video_path,omitempty"`
	NextSteps              []string    `json:"next_steps"`
}

// RenderResult 是 OpenMontage 渲染或增强渲染的结果。
type RenderResult struct {
	ProjectID             string          `json:"project_id"`
	Renderer              string          `json:"renderer"`
	Status                string          `json:"status"`
	Pipeline              string          `json:"pipeline"`
	PipelineManifestPath  string          `json:"pipeline_manifest_path,omitempty"`
	PropsPath             string          `json:"props_path"`
	RequestPath           string          `json:"request_path,omitempty"`
	VideoPath             string          `json:"video_path"`
	LogPath               string          `json:"log_path"`
	NarrationPath         string          `json:"narration_path,omitempty"`
	BackgroundMusicPath   string          `json:"background_music_path,omitempty"`
	SRTPath               string          `json:"srt_path,omitempty"`
	ASSPath               string          `json:"ass_path,omitempty"`
	ImagePaths            []string        `json:"image_paths,omitempty"`
	VideoPaths            []string        `json:"video_paths,omitempty"`
	ProviderCapabilities  map[string]bool `json:"provider_capabilities,omitempty"`
	FullPipelineSummary   string          `json:"full_pipeline_summary_path,omitempty"`
	OpenMontageProjectDir string          `json:"openmontage_project_dir,omitempty"`
	DurationS             int             `json:"duration_seconds"`
	GeneratedAt           time.Time       `json:"generated_at"`
	CommandSummary        string          `json:"command_summary"`
	Error                 string          `json:"error,omitempty"`
}

// RenderOpenMontageRequest 是调用 OpenMontage 渲染适配器的请求。
type RenderOpenMontageRequest struct {
	Pipeline                string   `json:"pipeline"`
	AspectRatio             string   `json:"aspect_ratio"`
	ImagePaths              []string `json:"image_paths"`
	VideoPaths              []string `json:"video_paths"`
	BackgroundMusicPath     string   `json:"background_music_path"`
	GenerateTTS             bool     `json:"generate_tts"`
	TTSVoice                string   `json:"tts_voice"`
	GenerateBackgroundMusic bool     `json:"generate_background_music"`
	GenerateSubtitles       bool     `json:"generate_subtitles"`
	GenerateAIImages        bool     `json:"generate_ai_images"`
	StockProviders          []string `json:"stock_providers"`
	ProviderMode            string   `json:"provider_mode"`
}

// VariantRequest 是生成 A/B 测试版本的请求。
type VariantRequest struct {
	Count int `json:"count"`
}

// PublicationMode 表示平台发布的官方接入方式。
type PublicationMode string

const (
	PublicationModeDirectAPI     PublicationMode = "direct_api"
	PublicationModeClientHandoff PublicationMode = "client_handoff"
	PublicationModeAssisted      PublicationMode = "assisted"
)

// PublicationStatus 区分已提交平台审核和仍需用户完成的官方操作。
type PublicationStatus string

const (
	PublicationStatusSubmitted      PublicationStatus = "submitted"
	PublicationStatusActionRequired PublicationStatus = "action_required"
	PublicationStatusFailed         PublicationStatus = "failed"
)

type PlatformPublishCapability struct {
	Platform                 Platform        `json:"platform"`
	Mode                     PublicationMode `json:"mode"`
	Configured               bool            `json:"configured"`
	SupportsVideo            bool            `json:"supports_video"`
	RequiresUserConfirmation bool            `json:"requires_user_confirmation"`
	Description              string          `json:"description"`
}

// PublishProjectRequest 可在一次用户确认后向一个或多个平台注册发布任务。
type PublishProjectRequest struct {
	Platform  Platform                     `json:"platform"`
	Platforms []Platform                   `json:"platforms"`
	VideoPath string                       `json:"video_path"`
	VideoURL  string                       `json:"video_url"`
	CoverPath string                       `json:"cover_path"`
	CoverURL  string                       `json:"cover_url"`
	Title     string                       `json:"title"`
	Caption   string                       `json:"caption"`
	Tags      []string                     `json:"tags"`
	Confirm   bool                         `json:"confirm"`
	Overrides map[Platform]PublishOverride `json:"overrides,omitempty"`
}

type PublishOverride struct {
	Title   string   `json:"title"`
	Caption string   `json:"caption"`
	Tags    []string `json:"tags"`
}

type PublicationResult struct {
	Platform      Platform          `json:"platform"`
	Mode          PublicationMode   `json:"mode"`
	Status        PublicationStatus `json:"status"`
	ExternalID    string            `json:"external_id,omitempty"`
	ExternalURL   string            `json:"external_url,omitempty"`
	ActionURL     string            `json:"action_url,omitempty"`
	Message       string            `json:"message"`
	ClientPayload map[string]any    `json:"client_payload,omitempty"`
	SubmittedAt   time.Time         `json:"submitted_at"`
}

type PublishProjectResult struct {
	ProjectID string              `json:"project_id"`
	Results   []PublicationResult `json:"results"`
}
