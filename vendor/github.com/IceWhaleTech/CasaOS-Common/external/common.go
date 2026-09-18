package external

import (
	"errors"
	"net/http"
	"os"
	"time"

	http2 "github.com/IceWhaleTech/CasaOS-Common/utils/http"
)

func getAddress(addressFile string) (string, error) {
	buf, err := os.ReadFile(addressFile)
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

func ping(address string, timeout time.Duration) error {
	response, err := http2.Get(address+"/ping", timeout)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return errors.New("failed to ping the service as address " + address)
	}

	return nil
}
