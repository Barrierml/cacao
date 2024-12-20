package api

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
)

func LoginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.String()
		if !strings.HasPrefix(path, "/api/") {
			c.Next()
			return
		}
		if path == "/api/user/register" || path == "/api/user/login" {
			c.Next()
			return
		}
		idstr, errid := c.Cookie("id")
		token, errtoken := c.Cookie("token")
		if errid != nil || errtoken != nil || len(idstr) == 0 || len(token) == 0 {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		id, err := strconv.ParseUint(idstr, 10, 64)
		if err != nil {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		user := &model.User{}
		user.ID = uint(id)

		db := storage.Get()
		result := db.Where(user).Take(user)
		if result.Error != nil || user.Token != token {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		c.Set("user", user)
		c.Next()
	}
}

func UserInfo(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	setResponseData(c, gin.H{
		"name":    user.Name,
		"role":    user.Role,
		"regtime": user.CreatedAt.Format(time.DateTime),
	})
}

func UserStatistics(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	setResponseData(c, gin.H{
		"netnum": uint(len(model.GetNetsByUserID(user.ID))),
		"devnum": uint(len(model.GetDevicesByUserID(user.ID))),
		"rxsum":  model.GetRxSumByUserID(user.ID),
		"txsum":  model.GetTxSumByUserID(user.ID),
	})
}

func UserRegister(c *gin.Context) {
	if model.GetConfig("openreg", "true") != "true" {
		setErrorCode(c, RegistrationDisabled)
		return
	}

	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if request.Username == "@" {
		setErrorCode(c, InvalidUsername)
		return
	}
	if !candy.IsValidUsername(request.Username) {
		setErrorCode(c, InvalidUsername)
		return
	}
	if len(request.Password) == 0 {
		setErrorCode(c, InvalidPassword)
		return
	}

	db := storage.Get()
	if func() bool {
		count := int64(0)
		db.Model(&model.User{}).Where(&model.User{IP: c.ClientIP(), Role: "normal"}).Where("created_at > ?", time.Now().Add(-1*registerInterval())).Count(&count)
		return count > 0
	}() {
		setErrorCode(c, RegisterTooOften)
		return
	}

	if func() bool {
		count := int64(0)
		db.Model(&model.User{}).Where(&model.User{Name: request.Username}).Count(&count)
		return count > 0
	}() {
		setErrorCode(c, UsernameAlreadyTaken)
		return
	}

	role := func() string {
		count := int64(0)
		db.Model(&model.User{}).Count(&count)
		if count == 0 {
			return "admin"
		}
		return "normal"
	}()

	user := model.User{
		Name:     request.Username,
		Password: hashUserPassword(request.Username, request.Password),
		Token:    uuid.NewString(),
		Role:     role,
		IP:       c.ClientIP(),
	}

	if result := db.Create(&user); result.Error != nil {
		setUnexpectedMessage(c, result.Error.Error())
		return
	}

	c.SetCookie("id", strconv.FormatUint(uint64(user.ID), 10), 86400, "/", "", false, true)
	c.SetCookie("token", user.Token, 86400, "/", "", false, true)

	setResponseData(c, gin.H{
		"name": user.Name,
		"role": user.Role,
	})

	if role == "admin" {
		model.SetConfig("openreg", "false")
	}

	if role == "normal" {
		netModel := &model.Net{
			UserID:    user.ID,
			Name:      "@",
			Password:  randomString(8),
			DHCP:      "192.168.202.0/24",
			Broadcast: true,
		}
		netModel.Create()
		candy.InsertNet(netModel)
	}
}

func UserLogin(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	user := model.User{
		Name:     request.Username,
		Password: hashUserPassword(request.Username, request.Password),
	}

	db := storage.Get()

	if result := db.Where(user).Take(&user); result.Error != nil {
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}

	if len(user.IP) == 0 {
		user.IP = c.ClientIP()
	}

	user.Token = uuid.NewString()
	user.Save()

	c.SetCookie("id", strconv.FormatUint(uint64(user.ID), 10), 86400, "/", "", false, true)
	c.SetCookie("token", user.Token, 86400, "/", "", false, true)

	setResponseData(c, gin.H{
		"name": user.Name,
		"role": user.Role,
	})
}

func UserLogout(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	user.Token = uuid.NewString()
	user.Save()

	c.SetCookie("id", "", -1, "/", "", false, true)
	c.SetCookie("token", "", -1, "/", "", false, true)

	setResponseData(c, nil)
}

func ChangePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old"`
		NewPassword string `json:"new"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	user := c.MustGet("user").(*model.User)

	if user.Password != hashUserPassword(user.Name, request.OldPassword) {
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}
	if len(request.NewPassword) == 0 {
		setErrorCode(c, InvalidPassword)
		return
	}

	user.Password = hashUserPassword(user.Name, request.NewPassword)
	user.Token = uuid.NewString()
	user.Save()

	c.SetCookie("id", strconv.FormatUint(uint64(user.ID), 10), 86400, "/", "", false, true)
	c.SetCookie("token", user.Token, 86400, "/", "", false, true)

	setResponseData(c, nil)
}

func registerInterval() time.Duration {
	intervalStr := model.GetConfig("reginterval", "1440")
	interval, err := strconv.Atoi(intervalStr)
	if err != nil {
		interval = 1440
	}
	return time.Duration(interval) * time.Minute
}

func hashUserPassword(username, password string) string {
	hash := sha256.Sum256([]byte(username + ":" + password))
	return fmt.Sprintf("%x", hash[:])
}

func randomString(n int) string {
	letters := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[r.Intn(len(letters))]
	}
	return string(b)
}
