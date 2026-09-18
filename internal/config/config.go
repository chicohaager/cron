package config

import "github.com/IceWhaleTech/CasaOS-Common/utils/constants"

type CommonModel struct {
	RuntimePath string
}

var CommonInfo = &CommonModel{
	RuntimePath: constants.DefaultRuntimePath,
}
