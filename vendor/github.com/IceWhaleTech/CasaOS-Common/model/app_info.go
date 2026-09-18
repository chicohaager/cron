package model

type ComposeAppWithStoreInfo struct {
	// Compose See [Compose Specification](https://compose-spec.io) for the schema structure of `ComposeApp`.
	Status          string              `json:"status,omitempty"`
	StoreInfo       ComposeAppStoreInfo `json:"store_info,omitempty"`
	UpdateAvailable bool                `json:"update_available,omitempty" yaml:",omitempty"`
}

type ComposeAppStoreInfo struct {
	Architectures  []string          `json:"architectures,omitempty" yaml:",omitempty"`
	Author         string            `json:"author"`
	Category       string            `json:"category"`
	Description    map[string]string `json:"description"`
	Developer      string            `json:"developer"`
	Hostname       string            `json:"hostname,omitempty" yaml:",omitempty"`
	Icon           string            `json:"icon"`
	Index          string            `json:"index" yaml:",omitempty"`
	Main           string            `json:"main,omitempty" yaml:",omitempty"`
	PortMap        string            `json:"port_map" mapstructure:"port_map" yaml:"port_map,omitempty"`
	Scheme         string            `json:"scheme,omitempty" yaml:",omitempty"`
	ScreenshotLink []string          `json:"screenshot_link" mapstructure:"screenshot_link" yaml:"screenshot_link,omitempty"`
	StoreAppID     string            `json:"store_app_id,omitempty" mapstructure:"store_app_id" yaml:"store_app_id,omitempty"`
	Tagline        map[string]string `json:"tagline"`
	Thumbnail      string            `json:"thumbnail"`
	Title          map[string]string `json:"title"`
}
