// Package config loads and validates the monitoring configuration from a YAML file.
//
// The config file defines thresholds, alerting settings, and the GitLab group to
// discover projects from. OCP resources (SAs, tokens, rolebindings) and GitLab
// projects are both auto-discovered at runtime and are NOT part of this config.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level monitoring configuration.
type Config struct {
	Thresholds    Thresholds  `yaml:"thresholds"`
	Alerting      Alerting    `yaml:"alerting"`
	GitLabBaseURL string      `yaml:"gitlabBaseURL"`
	Apps          []AppConfig `yaml:"apps"`
}

// AppConfig declares a single application to monitor.
// Name drives OCP resource discovery by naming convention
// (e.g. "pfm" → looks for pfm-image-builder SA, pfm-* namespaces).
// GitLabGroupID is the numeric ID of the app's GitLab sub-group;
// projects within that group are auto-discovered as repos.
type AppConfig struct {
	Name          string `yaml:"name"`
	GitLabGroupID int    `yaml:"gitlabGroupID"`
}

// Thresholds defines the health-check boundaries.
type Thresholds struct {
	TokenAgeWarningDays  int           `yaml:"tokenAgeWarningDays"`
	TokenAgeCriticalDays int           `yaml:"tokenAgeCriticalDays"`
	RunnerStalenessMin   int           `yaml:"runnerStalenessMinutes"`
	PipelineFailureWindow Duration     `yaml:"pipelineFailureWindow"`
}

// Alerting controls the email alerter behaviour.
type Alerting struct {
	EnableEmail        bool     `yaml:"enableEmail"`
	SMTPHost           string   `yaml:"smtpHost"`
	SMTPPort           int      `yaml:"smtpPort"`
	SenderAddress      string   `yaml:"senderAddress"`
	RecipientAddresses []string `yaml:"recipientAddresses"`
}

// Duration wraps time.Duration for YAML unmarshalling from strings like "24h".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// Load reads and validates a config file at the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	return Parse(data)
}

// Parse unmarshals YAML bytes into a Config and applies defaults/validation.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Thresholds.TokenAgeWarningDays == 0 {
		cfg.Thresholds.TokenAgeWarningDays = 60
	}
	if cfg.Thresholds.TokenAgeCriticalDays == 0 {
		cfg.Thresholds.TokenAgeCriticalDays = 90
	}
	if cfg.Thresholds.RunnerStalenessMin == 0 {
		cfg.Thresholds.RunnerStalenessMin = 10
	}
	if cfg.Thresholds.PipelineFailureWindow.Duration == 0 {
		cfg.Thresholds.PipelineFailureWindow.Duration = 24 * time.Hour
	}
	if cfg.Alerting.SMTPPort == 0 {
		cfg.Alerting.SMTPPort = 587
	}
}

func validate(cfg *Config) error {
	var errs []error

	if len(cfg.Apps) > 0 && cfg.GitLabBaseURL == "" {
		errs = append(errs, errors.New("gitlabBaseURL is required when apps are configured"))
	}

	for i, app := range cfg.Apps {
		if app.Name == "" {
			errs = append(errs, fmt.Errorf("apps[%d]: name is required", i))
		}
		if app.GitLabGroupID <= 0 {
			errs = append(errs, fmt.Errorf("apps[%d] (%s): gitlabGroupID must be a positive integer", i, app.Name))
		}
	}

	if cfg.Thresholds.TokenAgeWarningDays >= cfg.Thresholds.TokenAgeCriticalDays {
		errs = append(errs, fmt.Errorf(
			"tokenAgeWarningDays (%d) must be less than tokenAgeCriticalDays (%d)",
			cfg.Thresholds.TokenAgeWarningDays,
			cfg.Thresholds.TokenAgeCriticalDays,
		))
	}

	if cfg.Thresholds.RunnerStalenessMin < 1 {
		errs = append(errs, errors.New("runnerStalenessMinutes must be >= 1"))
	}

	if cfg.Alerting.EnableEmail {
		if cfg.Alerting.SMTPHost == "" {
			errs = append(errs, errors.New("alerting.smtpHost is required when email is enabled"))
		}
		if cfg.Alerting.SenderAddress == "" {
			errs = append(errs, errors.New("alerting.senderAddress is required when email is enabled"))
		}
		if len(cfg.Alerting.RecipientAddresses) == 0 {
			errs = append(errs, errors.New("alerting.recipientAddresses must have at least one entry when email is enabled"))
		}
	}

	return errors.Join(errs...)
}

