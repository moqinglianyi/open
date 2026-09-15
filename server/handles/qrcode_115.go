package handles

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

// Get115QRCode returns a fresh QR code session for the selected app.
//
// GET /api/admin/setting/get_115_qrcode?app=web
//
// Response:
//
//	{ uid, time, sign, image_url }
func Get115QRCode(c *gin.Context) {
	app := c.DefaultQuery("app", "web")
	_ = app // used only in confirmation step; any app can show the same QR

	client := driver115.New()
	session, err := client.QRCodeStart()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, gin.H{
		"uid":      session.UID,
		"time":     session.Time,
		"sign":     session.Sign,
		"image_url": fmt.Sprintf(driver115.ApiQrcodeImage, session.UID),
	})
}

// Check115QRCodeReq is the body for the status/confirm endpoint.
type Check115QRCodeReq struct {
	UID  string `json:"uid"  form:"uid"`
	Time int64  `json:"time" form:"time"`
	Sign string `json:"sign" form:"sign"`
	App  string `json:"app"  form:"app"`
}

// Check115QRCode polls QR code status and — when the user has scanned and
// confirmed — returns the 115 cookie string ready to paste into the driver
// configuration.
//
// POST /api/admin/setting/check_115_qrcode
//
// Response:
//
//	{ status: -2|-1|0|1|2, cookie: "UID=...;CID=...;SEID=...;KID=..." }
func Check115QRCode(c *gin.Context) {
	var req Check115QRCodeReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.App == "" {
		req.App = "web"
	}

	session := &driver115.QRCodeSession{
		UID:  req.UID,
		Time: req.Time,
		Sign: req.Sign,
	}

	client := driver115.New()
	status, err := client.QRCodeStatus(session)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// Not confirmed yet – return status only.
	if !status.IsAllowed() {
		common.SuccessResp(c, gin.H{"status": status.Status, "cookie": ""})
		return
	}

	// Status 2: user confirmed – exchange for credential.
	credential, err := client.QRCodeLoginWithApp(session, driver115.LoginApp(req.App))
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	cookie := fmt.Sprintf(
		"UID=%s;CID=%s;SEID=%s;KID=%s",
		credential.UID, credential.CID, credential.SEID, credential.KID,
	)
	common.SuccessResp(c, gin.H{"status": 2, "cookie": cookie})
}
