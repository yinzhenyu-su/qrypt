// Package driver preserves the old internal driver registration import path.
package driver

import _ "github.com/yinzhenyu/qrypt/pkg/drivers/all" // registers all drivers via their init functions
