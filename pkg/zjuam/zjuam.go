package zjuam

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

const loginUrl = "https://zjuam.zju.edu.cn/cas/login"
const publicKeyUrl = "https://zjuam.zju.edu.cn/cas/v2/getPubKey"

type PubKey struct {
	N *big.Int `json:"modulus"`
	E int64    `json:"exponent"`
}

type pubkeyRaw struct {
	N string `json:"modulus"`
	E string `json:"exponent"`
}

func newPubKey(modulus, exponent string) (PubKey, error) {
	p := PubKey{
		N: &big.Int{},
	}
	_, ok := p.N.SetString(modulus, 16)
	if !ok {
		return p, fmt.Errorf("failed to set modulus '%s'", modulus)
	}
	var err error
	p.E, err = strconv.ParseInt(exponent, 16, 64)
	if err != nil {
		return p, fmt.Errorf("failed to set exponent '%s'", exponent)
	}
	return p, nil
}

func (p *PubKey) encrypt(payload string) string {
	dst := make([]byte, hex.EncodedLen(len(payload)))
	hex.Encode(dst, []byte(payload))
	m := &big.Int{}
	_, _ = m.SetString(string(dst), 16)
	c := &big.Int{}
	c.Exp(m, big.NewInt(p.E), p.N)
	r := fmt.Sprintf("%x", c)
	paddingLen := 128 - len(r)
	if paddingLen > 0 {
		r = strings.Repeat("0", paddingLen) + r
	}
	return r
}

func extractCookieBody(c string) string {
	// c = "COOKIENAME=COOKIECONTENET; Path=/lol; Domain=azuk.top; HttpOnly
	idx := bytes.Index([]byte(c), []byte(";"))
	if idx == -1 {
		return c
	}
	return c[:idx+1]
}

func extractCookies(header http.Header) string {
	for k, v := range header {
		if k == "Set-Cookie" {
			var b strings.Builder
			for _, c := range v {
				b.WriteString(extractCookieBody(c))
			}
			return b.String()
		}
	}
	return ""
}

func (c *ZjuamClient) Login(ctx context.Context, payloadUrl, username, password string) error {
	// see https://github.com/determ1ne/ejector/blob/fbc10d91b5d450cfa9f94a6ef22916463c9107f1/Ejector/Services/ZjuService.cs#L44
	// stage 1: get csrf key
	lpRes, err := c.HttpClient.Get(payloadUrl)
	if err != nil {
		e := fmt.Sprintf("can not access login page: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	pageContent, err := io.ReadAll(lpRes.Body)
	lpRes.Body.Close()
	if err != nil {
		e := fmt.Sprintf("can not read login page: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	idxStart := bytes.Index(pageContent, []byte("execution\"")) + 18
	idxStop := bytes.Index(pageContent[idxStart:], []byte("\" />")) + idxStart
	csrf := pageContent[idxStart:idxStop]

	// stage 2: get pub key
	pkRes, err := c.HttpClient.Get(publicKeyUrl)
	if err != nil {
		e := fmt.Sprintf("can not access pubkey: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	pkContent, err := io.ReadAll(pkRes.Body)
	pkRes.Body.Close()
	if err != nil {
		e := fmt.Sprintf("can not read pubkey: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	var pkRaw pubkeyRaw
	err = json.Unmarshal(pkContent, &pkRaw)
	if err != nil {
		e := fmt.Sprintf("can not unmarshal pubkey: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	pk, err := newPubKey(pkRaw.N, pkRaw.E)
	if err != nil {
		e := fmt.Sprintf("can not create pubkey: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	encP := pk.encrypt(password)

	// stage 3: fire target
	lRes, err := c.HttpClient.PostForm(loginUrl, url.Values{
		"username":  {username},
		"password":  {encP},
		"authcode":  {""},
		"execution": {string(csrf)},
		"_eventId":  {"submit"},
	})
	if lRes == nil || err != nil {
		e := fmt.Sprintf("无法向zjuam提交表单: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New("无法向zjuam提交表单，怀疑是限制内网访问，请过段时间再来~")
	}
	content, err := io.ReadAll(lRes.Body)
	lRes.Body.Close()
	if err != nil {
		e := fmt.Sprintf("can not read login response: %s", err)
		log.Ctx(ctx).Error().Msg(e)
		return errors.New(e)
	}
	if lRes.StatusCode != http.StatusOK {
		return errors.New("http返回值错误，请检查用户名密码是否正确")
	}
	if strings.Contains(string(content), "用户名或密码错误") {
		return errors.New("用户名或密码错误")
	}
	// 不代表登录成功(大概率成功)
	return nil
}

func (c *ZjuamClient) Client() *http.Client {
	if c.HttpClient == nil {
		jar, _ := cookiejar.New(nil)
		c.HttpClient = &http.Client{Jar: jar}
	}
	return c.HttpClient
}
