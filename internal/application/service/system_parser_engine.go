package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

var systemParserEngineNames = []string{
	docparser.MinerUEngineName, docparser.MinerUCloudEngineName,
	docparser.PaddleOCRVLEngineName, docparser.PaddleOCRVLCloudEngineName,
	docparser.WeKnoraCloudEngineName, docparser.PDFSpecializedEngineName,
}

// SystemParserEngineService manages and resolves platform-owned parser configurations.
type SystemParserEngineService struct {
	repo interfaces.SystemParserEngineRepository
}

// NewSystemParserEngineService creates the platform parser configuration service.
func NewSystemParserEngineService(repo interfaces.SystemParserEngineRepository) *SystemParserEngineService {
	return &SystemParserEngineService{repo: repo}
}

func systemParserRegistration(name string) (docparser.EngineRegistration, error) {
	for _, engine := range systemParserEngineNames {
		if engine == name {
			if registration, ok := docparser.LookupEngine(name); ok {
				return registration, nil
			}
		}
	}
	return nil, apperrors.NewBadRequestError("Unsupported system parser engine: " + name)
}

func systemParserInfo(registration docparser.EngineRegistration) types.ParserEngineInfo {
	return types.ParserEngineInfo{
		Name: registration.Name(), Description: registration.Description(),
		FileTypes: registration.FileTypes(false), Available: true,
	}
}

// List returns only redacted configuration, including engines not yet configured.
func (s *SystemParserEngineService) List(ctx context.Context) ([]types.SystemParserEngineView, error) {
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]types.SystemParserEngine, len(records))
	for _, record := range records {
		byName[record.Engine] = record
	}
	result := make([]types.SystemParserEngineView, 0, len(systemParserEngineNames))
	for _, name := range systemParserEngineNames {
		registration, err := systemParserRegistration(name)
		if err != nil {
			return nil, err
		}
		record := byName[name]
		cfg, err := decodeSystemParserConfig(record.Config)
		if err != nil {
			return nil, err
		}
		configured := systemParserConfigured(name, cfg)
		info := systemParserInfo(registration)
		info.Available = record.Enabled && configured
		for _, secret := range systemParserSecrets(&cfg) {
			if *secret != "" {
				*secret = types.RedactedSecretPlaceholder
			}
		}
		result = append(result, types.SystemParserEngineView{
			ParserEngineInfo: info, Enabled: record.Enabled,
			Configured: configured, Config: cfg,
		})
	}
	return result, nil
}

// Available exposes metadata only. Availability means configured and enabled;
// external connectivity is tested explicitly, not on each catalog request.
func (s *SystemParserEngineService) Available(ctx context.Context) ([]types.ParserEngineInfo, error) {
	engines, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]types.ParserEngineInfo, 0, len(engines))
	for _, engine := range engines {
		if engine.Enabled && engine.Configured {
			result = append(result, engine.ParserEngineInfo)
		}
	}
	return result, nil
}

func decodeSystemParserConfig(data types.JSON) (types.SystemParserEngineConfig, error) {
	var cfg types.SystemParserEngineConfig
	if len(data) == 0 {
		return cfg, nil
	}
	err := json.Unmarshal(data, &cfg)
	return cfg, err
}

func systemParserSecrets(cfg *types.SystemParserEngineConfig) []*string {
	return []*string{
		&cfg.MinerUSelfHostedAPIKey, &cfg.MinerUAPIKey,
		&cfg.PaddleOCRVLCloudToken, &cfg.WeKnoraCloudAppSecret,
	}
}

// selectSystemParserConfig keeps only this engine's parameters. In particular,
// credentials belonging to another service must never reach a parser request.
func selectSystemParserConfig(name string, cfg types.SystemParserEngineConfig) types.SystemParserEngineConfig {
	var selected types.SystemParserEngineConfig
	switch name {
	case docparser.MinerUEngineName:
		selected.ParserEngineConfig = types.ParserEngineConfig{
			MinerUEndpoint: strings.TrimSpace(cfg.MinerUEndpoint), MinerUSelfHostedAPIKey: cfg.MinerUSelfHostedAPIKey,
			MinerUModel: cfg.MinerUModel, MinerUVLMServerURL: strings.TrimSpace(cfg.MinerUVLMServerURL),
			MinerUEnableFormula: cfg.MinerUEnableFormula, MinerUEnableTable: cfg.MinerUEnableTable,
			MinerUParseMethod: cfg.MinerUParseMethod, MinerULanguage: cfg.MinerULanguage,
		}
	case docparser.MinerUCloudEngineName:
		selected.ParserEngineConfig = types.ParserEngineConfig{
			MinerUAPIKey: cfg.MinerUAPIKey, MinerUCloudModel: cfg.MinerUCloudModel,
			MinerUCloudEnableFormula: cfg.MinerUCloudEnableFormula, MinerUCloudEnableTable: cfg.MinerUCloudEnableTable,
			MinerUCloudEnableOCR: cfg.MinerUCloudEnableOCR, MinerUCloudLanguage: cfg.MinerUCloudLanguage,
		}
	case docparser.PaddleOCRVLEngineName:
		selected.ParserEngineConfig = types.ParserEngineConfig{
			PaddleOCRVLEndpoint:            strings.TrimSpace(cfg.PaddleOCRVLEndpoint),
			PaddleOCRVLUseSealRecognition:  cfg.PaddleOCRVLUseSealRecognition,
			PaddleOCRVLUseChartRecognition: cfg.PaddleOCRVLUseChartRecognition,
		}
	case docparser.PaddleOCRVLCloudEngineName:
		selected.ParserEngineConfig = types.ParserEngineConfig{
			PaddleOCRVLCloudToken: cfg.PaddleOCRVLCloudToken, PaddleOCRVLCloudModel: cfg.PaddleOCRVLCloudModel,
			PaddleOCRVLCloudUseSealRecognition:  cfg.PaddleOCRVLCloudUseSealRecognition,
			PaddleOCRVLCloudUseChartRecognition: cfg.PaddleOCRVLCloudUseChartRecognition,
		}
	case docparser.WeKnoraCloudEngineName:
		selected.WeKnoraCloudAppID = strings.TrimSpace(cfg.WeKnoraCloudAppID)
		selected.WeKnoraCloudAppSecret = cfg.WeKnoraCloudAppSecret
	case docparser.PDFSpecializedEngineName:
		selected.PDFSpecializedEndpoint = strings.TrimSpace(cfg.PDFSpecializedEndpoint)
	}
	return selected
}

func systemParserConfigured(name string, cfg types.SystemParserEngineConfig) bool {
	switch name {
	case docparser.MinerUEngineName:
		return cfg.MinerUEndpoint != ""
	case docparser.MinerUCloudEngineName:
		return cfg.MinerUAPIKey != ""
	case docparser.PaddleOCRVLEngineName:
		return cfg.PaddleOCRVLEndpoint != ""
	case docparser.PaddleOCRVLCloudEngineName:
		return cfg.PaddleOCRVLCloudToken != ""
	case docparser.WeKnoraCloudEngineName:
		return cfg.WeKnoraCloudAppID != "" && cfg.WeKnoraCloudAppSecret != ""
	case docparser.PDFSpecializedEngineName:
		return cfg.PDFSpecializedEndpoint != ""
	}
	return false
}

func (s *SystemParserEngineService) prepareConfig(
	ctx context.Context, name string, incoming types.SystemParserEngineConfig,
) (types.SystemParserEngineConfig, error) {
	if _, err := systemParserRegistration(name); err != nil {
		return incoming, err
	}
	cfg := selectSystemParserConfig(name, incoming)
	record, err := s.repo.Get(ctx, name)
	if err != nil {
		return cfg, err
	}
	var previous types.SystemParserEngineConfig
	if record != nil {
		previous, err = decodeSystemParserConfig(record.Config)
		if err != nil {
			return cfg, err
		}
	}
	oldSecrets := systemParserSecrets(&previous)
	for i, secret := range systemParserSecrets(&cfg) {
		*secret = types.PreserveIfRedacted(strings.TrimSpace(*secret), *oldSecrets[i])
	}
	if err := validateParserEngineOverrideURLs(cfg.ToOverridesMap()); err != nil {
		return cfg, apperrors.NewBadRequestError(err.Error())
	}
	if cfg.PDFSpecializedEndpoint != "" {
		if err := utils.ValidateURLForSSRF(cfg.PDFSpecializedEndpoint); err != nil {
			return cfg, apperrors.NewBadRequestError("Invalid PDF parser endpoint: " + err.Error())
		}
	}
	return cfg, nil
}

// Save validates and encrypts one engine's configuration before persisting it.
func (s *SystemParserEngineService) Save(
	ctx context.Context, name string, enabled bool, incoming types.SystemParserEngineConfig,
) error {
	cfg, err := s.prepareConfig(ctx, name, incoming)
	if err != nil {
		return err
	}
	if enabled && !systemParserConfigured(name, cfg) {
		return apperrors.NewBadRequestError("Complete the system parser configuration before enabling it")
	}
	for _, secret := range systemParserSecrets(&cfg) {
		if *secret == "" || strings.HasPrefix(*secret, utils.EncPrefix) {
			continue
		}
		key := utils.GetAESKey()
		if key == nil {
			return fmt.Errorf("encrypt system parser credentials: SYSTEM_AES_KEY must be exactly 32 bytes")
		}
		*secret, err = utils.EncryptAESGCM(*secret, key)
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.repo.Upsert(ctx, &types.SystemParserEngine{Engine: name, Enabled: enabled, Config: types.JSON(data)})
}

func decryptSystemParserConfig(cfg *types.SystemParserEngineConfig) error {
	for _, secret := range systemParserSecrets(cfg) {
		value, err := utils.DecryptStoredSecret(*secret)
		if err != nil {
			return fmt.Errorf("decrypt system parser credentials: %w", err)
		}
		*secret = value
	}
	return nil
}

// Check probes one supported engine with draft parameters and saved secret placeholders.
func (s *SystemParserEngineService) Check(
	ctx context.Context, name string, incoming types.SystemParserEngineConfig,
) (types.ParserEngineInfo, error) {
	registration, err := systemParserRegistration(name)
	if err != nil {
		return types.ParserEngineInfo{}, err
	}
	cfg, err := s.prepareConfig(ctx, name, incoming)
	if err != nil {
		return types.ParserEngineInfo{}, err
	}
	if !systemParserConfigured(name, cfg) {
		return types.ParserEngineInfo{}, apperrors.NewBadRequestError("System parser configuration is incomplete")
	}
	if err := decryptSystemParserConfig(&cfg); err != nil {
		return types.ParserEngineInfo{}, err
	}
	info := systemParserInfo(registration)
	// These services have no credential-validation endpoint in the parser
	// contract. Do not report a configuration-only check as connectivity.
	if name == docparser.WeKnoraCloudEngineName || name == docparser.PDFSpecializedEngineName {
		return info, apperrors.NewBadRequestError("This parser does not support a connection check")
	}
	info.Available, info.UnavailableReason = registration.CheckAvailable(false, cfg.ToOverridesMap())
	return info, nil
}

// Resolve validates the current switch on every new parse, then decrypts only
// the selected engine's configuration. It never consults workspace settings.
func (s *SystemParserEngineService) Resolve(
	ctx context.Context, name, fileType string,
) (types.SystemParserEngineConfig, error) {
	var cfg types.SystemParserEngineConfig
	registration, err := systemParserRegistration(name)
	if err != nil {
		return cfg, err
	}
	supported := false
	for _, candidate := range registration.FileTypes(false) {
		if candidate == fileType {
			supported = true
			break
		}
	}
	if !supported {
		return cfg, apperrors.NewBadRequestError("System parser does not support file type: " + fileType)
	}
	record, err := s.repo.Get(ctx, name)
	if err != nil {
		return cfg, err
	}
	if record == nil || !record.Enabled {
		return cfg, apperrors.NewBadRequestError("System parser is disabled or not configured: " + name)
	}
	cfg, err = decodeSystemParserConfig(record.Config)
	if err != nil {
		return cfg, err
	}
	if !systemParserConfigured(name, cfg) {
		return cfg, apperrors.NewBadRequestError("System parser configuration is incomplete: " + name)
	}
	if err := decryptSystemParserConfig(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validateParserEngineSource(rule types.ParserEngineRule, allowSystem bool) error {
	switch rule.Source {
	case "", "tenant":
		return nil
	case types.ParserEngineSourceSystem:
		if allowSystem {
			return nil
		}
	}
	return apperrors.NewBadRequestError("Unsupported parser engine source: " + rule.Source)
}

func validateTenantParserRules(rules []types.ParserEngineRule) error {
	for _, rule := range rules {
		if err := validateParserEngineSource(rule, false); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRules permits retaining a disabled selection while editing other KB
// settings, but only enabled configurations may be newly selected.
func (s *SystemParserEngineService) ValidateRules(
	ctx context.Context, rules, previous []types.ParserEngineRule,
) error {
	oldConfig := types.ChunkingConfig{ParserEngineRules: previous}
	for _, rule := range rules {
		if err := validateParserEngineSource(rule, true); err != nil {
			return err
		}
		if rule.Source != types.ParserEngineSourceSystem {
			continue
		}
		if _, err := systemParserRegistration(rule.Engine); err != nil {
			return err
		}
		for _, fileType := range rule.FileTypes {
			oldRule := oldConfig.ResolveParserEngineRule(fileType)
			if oldRule != nil && oldRule.Engine == rule.Engine && oldRule.Source == rule.Source {
				continue
			}
			if _, err := s.Resolve(ctx, rule.Engine, normalizeParserFileType(fileType)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateSelection checks the effective rule before an import or destructive reparse.
func (s *SystemParserEngineService) ValidateSelection(
	ctx context.Context, config types.ChunkingConfig, fileType string,
) error {
	rule := config.ResolveParserEngineRule(fileType)
	if rule == nil {
		return nil
	}
	if err := validateParserEngineSource(*rule, true); err != nil {
		return err
	}
	if rule.Source == types.ParserEngineSourceSystem {
		_, err := s.Resolve(ctx, rule.Engine, normalizeParserFileType(fileType))
		return err
	}
	return nil
}

// NewReader constructs a reader with only the selected platform configuration.
func (s *SystemParserEngineService) NewReader(
	ctx context.Context, rule types.ParserEngineRule, fileType string,
) (interfaces.DocReader, map[string]string, error) {
	cfg, err := s.Resolve(ctx, rule.Engine, normalizeParserFileType(fileType))
	if err != nil {
		return nil, nil, err
	}
	overrides := cfg.ToOverridesMap()
	switch rule.Engine {
	case docparser.WeKnoraCloudEngineName:
		reader, err := docparser.NewWeKnoraCloudSignedDocumentReader(cfg.WeKnoraCloudAppID, cfg.WeKnoraCloudAppSecret)
		return reader, overrides, err
	case docparser.PDFSpecializedEngineName:
		return docparser.NewSafePDFSpecializedReader(cfg.PDFSpecializedEndpoint), overrides, nil
	default:
		reader, err := docparser.NewReader(ctx, rule.Engine, fileType, false,
			docparser.ReaderDeps{Overrides: overrides})
		return reader, overrides, err
	}
}
