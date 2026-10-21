package types

// SystemParserEngine stores one platform-managed configuration per engine.
// Config contains encrypted credentials and must never be returned directly.
type SystemParserEngine struct {
	Engine  string `gorm:"primaryKey;size:64" json:"engine"`
	Enabled bool   `json:"enabled"`
	Config  JSON   `gorm:"type:jsonb;not null" json:"-"`
}

// SystemParserEngineConfig reuses the existing parser parameters, with the
// credentials and endpoint needed by platform-managed external services.
type SystemParserEngineConfig struct {
	ParserEngineConfig
	WeKnoraCloudAppID      string `json:"weknoracloud_app_id,omitempty"`
	WeKnoraCloudAppSecret  string `json:"weknoracloud_app_secret,omitempty"`
	PDFSpecializedEndpoint string `json:"pdf_specialized_endpoint,omitempty"`
}

// SystemParserEngineView is the admin projection; Config is always redacted.
type SystemParserEngineView struct {
	ParserEngineInfo
	Enabled    bool                     `json:"enabled"`
	Configured bool                     `json:"configured"`
	Config     SystemParserEngineConfig `json:"config"`
}
