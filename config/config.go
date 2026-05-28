package config

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/pkg/database"
	"github.com/chaunsin/netease-cloud-music/pkg/log"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var HomeDir string

var (
	//go:embed config.yaml
	defaultConfigByte []byte
	defaultConfig     *Config
)

func init() {
	var err error
	HomeDir, err = os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	if err := yaml.Unmarshal(defaultConfigByte, &defaultConfig); err != nil {
		panic(fmt.Sprintf("defaultConfig.Unmarshal: %s", err))
	}
	// defaultConfig.ReplaceMagicVariables("HOME", HomeDir)
	if err := defaultConfig.Validate(); err != nil {
		panic(fmt.Sprintf("defaultConfig.Validate: %s", err))
	}
}

type Config struct {
	v           *viper.Viper
	Version     string           `json:"version" yaml:"version"`
	Log         *log.Config      `json:"log" yaml:"log"`
	Network     *api.Config      `json:"network" yaml:"network"`
	Database    *database.Config `json:"database" yaml:"database"`
	MusicianVip *MusicianVipConf `json:"musicianVip" yaml:"musicianVip"`
}

// MusicianVipConf 音乐人黑胶会员任务配置
type MusicianVipConf struct {
	// 笔记任务配置 - 发布图文笔记天数
	Note MusicianVipNoteConf `json:"note" yaml:"note"`
	// 播放任务配置 - 近30天有效播放次数
	Play MusicianVipPlayConf `json:"play" yaml:"play"`
}

// MusicianVipNoteConf 笔记发布任务配置
type MusicianVipNoteConf struct {
	// 笔记内容（支持多条，随机选择）
	Messages []string `json:"messages" yaml:"messages"`
	// 图片URL列表（支持多条，随机选择）
	ImageURLs []string `json:"imageUrls" yaml:"imageUrls"`
	// 动态类型: 35=普通动态
	Type int `json:"type" yaml:"type"`
}

// MusicianVipPlayConf 播放任务配置
type MusicianVipPlayConf struct {
	// 歌曲ID列表
	IDs string `json:"ids" yaml:"ids"`
	// 歌曲ID文件路径
	IDsFile string `json:"idsFile" yaml:"idsFile"`
	// 每次播放数量
	Num int64 `json:"num" yaml:"num"`
	// 两首歌之间最小间隔秒数
	GapMin int64 `json:"gapMin" yaml:"gapMin"`
	// 两首歌之间最大间隔秒数
	GapMax int64 `json:"gapMax" yaml:"gapMax"`
	// 非默认cookie文件路径（用于playids任务）
	CookieFile string `json:"cookieFile" yaml:"cookieFile"`
}

func (c *Config) Validate() error {
	return nil
}

func GetDefault() *Config {
	return defaultConfig
}

func New(cfgPath ...string) (*Config, error) {
	var (
		conf Config
		opts = viper.DecodeHook(func(m *mapstructure.DecoderConfig) {
			m.TagName = "yaml"
		})
		_cfgPath string
	)
	if len(cfgPath) > 0 {
		_cfgPath = cfgPath[0]
	}

	v := viper.New()
	v.SetTypeByDefaultValue(true)
	v.SetEnvPrefix("ncmctl")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.AllowEmptyEnv(true)
	v.SetConfigType("yaml")
	v.SetConfigFile(_cfgPath)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("ReadInConfig: %w", err)
	}
	if err := v.UnmarshalExact(&conf, opts); err != nil {
		return nil, fmt.Errorf("UnmarshalExact: %w", err)
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	return &conf, nil
}

// ReplaceMagicVariables 替换配置文件中的魔法变量。注意该方法只能调用一次再次调用则不会生效.
func (c *Config) ReplaceMagicVariables(name, value string) (*Config, bool) {

	var (
		isset   bool
		mapping = func(k string) string {
			switch k {
			case name:
				isset = true
				return value
			}
			return ""
		}
	)

	c.Log.Rotate.Filename = os.Expand(c.Log.Rotate.Filename, mapping)
	c.Network.Cookie.Filepath = os.Expand(c.Network.Cookie.Filepath, mapping)
	c.Database.Path = os.Expand(c.Database.Path, mapping)
	return c, isset
}
