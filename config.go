package main

import (
	"io/ioutil"
	"net/url"
	"os"
	"strings"

	"github.com/quiq/docker-registry-ui/registry"
	"gopkg.in/yaml.v2"
)

type configData struct {
	ListenAddr            string   `yaml:"listen_addr"`
	BasePath              string   `yaml:"base_path"`
	RegistryURL           string   `yaml:"registry_url"`
	VerifyTLS             bool     `yaml:"verify_tls"`
	Username              string   `yaml:"registry_username"`
	Password              string   `yaml:"registry_password"`
	PasswordFile          string   `yaml:"registry_password_file"`
	EventListenerToken    string   `yaml:"event_listener_token"`
	EventRetentionDays    int      `yaml:"event_retention_days"`
	EventDatabaseDriver   string   `yaml:"event_database_driver"`
	EventDatabaseLocation string   `yaml:"event_database_location"`
	EventDeletionEnabled  bool     `yaml:"event_deletion_enabled"`
	CacheRefreshInterval  uint8    `yaml:"cache_refresh_interval"`
	AnyoneCanDelete       bool     `yaml:"anyone_can_delete"`
	AnyoneCanViewEvents   bool     `yaml:"anyone_can_view_events"`
	Admins                []string `yaml:"admins"`
	Debug                 bool     `yaml:"debug"`
	PurgeTagsKeepDays     int      `yaml:"purge_tags_keep_days"`
	PurgeTagsKeepCount    int      `yaml:"purge_tags_keep_count"`
	PurgeTagsKeepRegexp   string   `yaml:"purge_tags_keep_regexp"`
	PurgeTagsKeepFromFile string   `yaml:"purge_tags_keep_from_file"`
	PurgeTagsSchedule     string   `yaml:"purge_tags_schedule"`

	PurgeConfig *registry.PurgeTagsConfig
}

func readConfig(configFile string) *configData {
	var config configData
	// Read config file.
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		panic(err)
	}
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		panic(err)
	}

	// Validate registry URL.
	if _, err := url.Parse(config.RegistryURL); err != nil {
		panic(err)
	}

	// Normalize base path.
	config.BasePath = strings.Trim(config.BasePath, "/")
	if config.BasePath != "" {
		config.BasePath = "/" + config.BasePath
	}

	// Read password from file.
	if config.PasswordFile != "" {
		if _, err := os.Stat(config.PasswordFile); os.IsNotExist(err) {
			panic(err)
		}
		data, err := ioutil.ReadFile(config.PasswordFile)
		if err != nil {
			panic(err)
		}
		config.Password = strings.TrimSuffix(string(data[:]), "\n")
	}

	config.PurgeConfig = &registry.PurgeTagsConfig{
		KeepDays:      config.PurgeTagsKeepDays,
		KeepMinCount:  config.PurgeTagsKeepCount,
		KeepTagRegexp: config.PurgeTagsKeepRegexp,
		KeepFromFile:  config.PurgeTagsKeepFromFile,
	}
	return &config
}
