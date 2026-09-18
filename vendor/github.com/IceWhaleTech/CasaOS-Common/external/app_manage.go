package external

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IceWhaleTech/CasaOS-Common/model"
	http2 "github.com/IceWhaleTech/CasaOS-Common/utils/http"
	"github.com/tidwall/gjson"
)

const (
	AppManageURLFilename = "app-management.url"
	APIComposeInfo       = "/v2/app_management/compose"
	APIComposeStatus     = "/v2/app_management/compose"
)

type AppManageService interface {
	GetAppInfo(storeId string) (model.ComposeAppWithStoreInfo, error)
	PutAppStatus(storeId string, status string) (bool, error)
}

type appManageService struct {
	address string
}

func (m *appManageService) GetAppInfo(storeId string) (model.ComposeAppWithStoreInfo, error) {
	url := strings.TrimSuffix(m.address, "/") + APIComposeInfo + "/" + storeId
	model := model.ComposeAppWithStoreInfo{}
	response, err := http2.Get(url, 5*time.Second)
	if err != nil {
		return model, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return model, errors.New("failed to create route (status code: " + fmt.Sprint(response.StatusCode) + ")")
	}
	str, err := io.ReadAll(response.Body)
	if err != nil {
		return model, err
	}
	defer response.Body.Close()
	gstr := gjson.Get(string(str), "data")
	err = json.Unmarshal([]byte(gstr.Raw), &model)
	return model, err
}

func (m *appManageService) PutAppStatus(storeId string, status string) (bool, error) {
	url := strings.TrimSuffix(m.address, "/") + APIComposeStatus + "/" + storeId + "/status"

	body := []byte(`"` + status + `"`)
	response, err := http2.Put(url, body, 5*time.Second)
	if err != nil {
		return false, err
	}
	if response.StatusCode != http.StatusOK {
		return false, errors.New("failed to change status (status code: " + fmt.Sprint(response.StatusCode) + ")")
	}
	return true, nil
}

func NewAppManageService(RuntimePath string) (AppManageService, error) {
	managementAddressFile := filepath.Join(RuntimePath, AppManageURLFilename)

	retry := 10

	for retry > 0 {
		if _, err := os.Stat(managementAddressFile); err == nil {
			break
		}

		fmt.Printf("gateway management address file `%s` not found, retrying in 1 second...(%d)\n", managementAddressFile, retry)

		time.Sleep(1 * time.Second)

		retry--
	}

	address, err := getAddress(managementAddressFile)
	if err != nil {
		return nil, err
	}

	if err := ping(address, 5*time.Second); err != nil {
		return nil, err
	}

	return &appManageService{
		address: address,
	}, nil
}
