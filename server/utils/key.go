package utils

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/crypto"
)

func FetchPrivKey(config *model.Config) (crypto.PrivKey, error) {
	var priv crypto.PrivKey
	var privateKeyFile *os.File
	var err error
	privateKeyFile, err = os.Open(config.GetDataPath("priv.key"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			priv, _, err = crypto.GenerateKeyPair(
				crypto.Ed25519,
				-1,
			)
			var privBytes []byte
			privBytes, err = crypto.MarshalPrivateKey(priv)
			if err != nil {
				return nil, err
			}
			fmt.Println("Generated private key")
			if _, err = os.Stat(config.DataDir); os.IsNotExist(err) {
				err = os.MkdirAll(config.DataDir, 0666)
				if err != nil {
					return nil, err
				}
			}
			privateKeyFile, err = os.Create(config.GetDataPath("priv.key"))
			if err != nil {
				return nil, err
			}
			_, err = privateKeyFile.Write(privBytes)
			err = privateKeyFile.Close()
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	} else {
		var privBytes []byte
		privBytes, err = io.ReadAll(privateKeyFile)
		if err != nil {
			return nil, err
		}
		priv, err = crypto.UnmarshalPrivateKey(privBytes)
		if err != nil {
			return nil, err
		}
		err = privateKeyFile.Close()
		if err != nil {
			return nil, err
		}
	}
	return priv, nil
}
