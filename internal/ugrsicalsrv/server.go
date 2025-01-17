package ugrsicalsrv

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/template"
	"time"

	"ugrs-ical/pkg/zjuservice"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/rs/zerolog/log"
)

const defaultServerConfigPath = "configs/server.json"

var _serverConfig ServerConfig

type YearAndSemester struct {
	Year     string `json:"year"`
	Semester string `json:"semester"`
}
type SetupData struct {
	Classes      []YearAndSemester
	Exams        []YearAndSemester
	Link         string
	SubLink      string
	ScoreSubLink string
}

type ServerConfig struct {
	Enckey    string `json:"enckey"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	CfgPath   string `json:"config"`
	IpHeader  string `json:"ip_header"`
	RedisAddr string `json:"redis_addr"`
	RedisPass string `json:"redis_pass"`
	CacheTTL  int    `json:"cache_ttl"`
}

var setupTpl *template.Template

var sd = SetupData{
	Classes:      []YearAndSemester{},
	Link:         "",
	SubLink:      "",
	ScoreSubLink: "",
}
var gcm cipher.AEAD
var rc *redis.Client
var cacheTTL time.Duration

func loadServerConfig() error {
	var r io.Reader
	f, err := os.Open(defaultServerConfigPath)
	defer f.Close()
	r = f
	if err != nil {
		return err
	}
	cfd, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	err = json.Unmarshal(cfd, &_serverConfig)
	return err
}

func validConfig() error {
	if len(_serverConfig.Enckey) == 0 || len(_serverConfig.Host) == 0 || _serverConfig.Port == 0 {
		return errors.New("invalid server config file, please check enckey,host and port fields")
	}
	return nil
}

func ListenAndServe() error {
	if err := loadServerConfig(); err != nil {
		return err
	}
	if err := validConfig(); err != nil {
		return err
	}
	c, err := aes.NewCipher([]byte(_serverConfig.Enckey))
	if err != nil {
		return err
	}
	gcm, err = cipher.NewGCM(c)
	if err != nil {
		return err
	}

	if _serverConfig.IpHeader != "" {
		log.Info().Msgf("ugrsicalsrv will get header from %s", _serverConfig.IpHeader)
	}

	if _serverConfig.RedisAddr == "" {
		log.Warn().Msg("redis not set, rate limit won't work")
	} else {
		rc = redis.NewClient(&redis.Options{
			Addr:     _serverConfig.RedisAddr,
			Password: _serverConfig.RedisPass,
			DB:       0,
		})
		pong, err := rc.Ping(context.Background()).Result()
		if err != nil {
			log.Error().Msgf("redis ping error: %s", err)
			return err
		}
		log.Info().Msgf("redis ping: %s", pong)
	}

	if _serverConfig.CacheTTL == 0 {
		cacheTTL = DurationIcalCache
	} else if _serverConfig.CacheTTL < 0 {
		return errors.New("cache ttl must be positive")
	} else {
		cacheTTL = time.Duration(_serverConfig.CacheTTL) * time.Hour
	}
	log.Info().Msgf("cache ttl: %f", cacheTTL.Hours())

	// read template
	f, err := os.Open("./web/template/setup.html")
	if err != nil {
		return err
	}
	fc, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	setupTpl, err = template.New("setup").Parse(string(fc))
	if err != nil {
		return err
	}
	// read config
	if err = zjuservice.LoadConfig(_serverConfig.CfgPath); err != nil {
		return err
	}

	cfg := zjuservice.GetConfig()
	if err != nil {
		return err
	}
	//TODO check config
	//当前生成的学期
	classes := make([]YearAndSemester, 0)
	for _, item := range cfg.ClassTerms {
		splits := strings.Split(item, ":")
		classes = append(classes, YearAndSemester{
			Year: splits[0],
			// convert like "1" to "冬学期"
			Semester: zjuservice.ClassTermStrToStr(splits[1]),
		})
	}
	sd.Classes = classes

	//当前生成的考试
	exams := make([]YearAndSemester, 0)
	for _, item := range cfg.ExamTerms {
		splits := strings.Split(item, ":")
		exams = append(exams, YearAndSemester{
			Year: splits[0],
			// convert like "1" to "春夏学期"
			Semester: zjuservice.ExamStrToStr(splits[1]),
		})
	}
	sd.Exams = exams

	app := gin.New()
	app.Use(gin.Logger())
	app.Use(gin.Recovery())

	setRoutes(app)

	log.Ctx(context.Background()).Info().Msgf("[server] running on %d", _serverConfig.Port)

	return app.Run(fmt.Sprintf(":%d", _serverConfig.Port))

}

func setRoutes(app *gin.Engine) {
	app.Use(RateLimiterM())
	app.GET("/", func(c *gin.Context) {
		c.Redirect(302, "/static")
	})
	app.GET("/ical", FetchCal)
	app.GET("/sub", SubCal)
	app.GET("/subScore", SubScore)
	app.GET("/score", FetchScore)
	app.GET("/ping", PingEp)
	app.POST("/static", SetupPage)
	app.Static("/static", "./web/app")

}
