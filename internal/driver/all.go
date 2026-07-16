// Package driver registers every storage driver shipped with qrypt.
package driver

import (
	_ "github.com/yinzhenyu/qrypt/internal/driver/aliyundrive"
	_ "github.com/yinzhenyu/qrypt/internal/driver/baidunetdisk"
	_ "github.com/yinzhenyu/qrypt/internal/driver/localfs"
	_ "github.com/yinzhenyu/qrypt/internal/driver/onedrive"
	_ "github.com/yinzhenyu/qrypt/internal/driver/p115"
	_ "github.com/yinzhenyu/qrypt/internal/driver/p189"
	_ "github.com/yinzhenyu/qrypt/internal/driver/quark"
	_ "github.com/yinzhenyu/qrypt/internal/driver/s3"
	_ "github.com/yinzhenyu/qrypt/internal/driver/webdav"
	_ "github.com/yinzhenyu/qrypt/internal/driver/yun139"
)
