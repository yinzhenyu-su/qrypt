// Package all registers every storage driver shipped with qrypt.
package all

import (
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/aliyundrive"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/baidunetdisk"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/onedrive"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/p115"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/p189"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/quark"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/s3"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/webdav"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/yun139"
)
