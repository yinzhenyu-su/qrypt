package p189

import "encoding/xml"

type xmlListFiles struct {
	XMLName  xml.Name       `xml:"listFiles"`
	FileList xmlFileList    `xml:"fileList"`
}

type xmlFileList struct {
	Count   int          `xml:"count"`
	Folders []xmlFolder  `xml:"folder"`
	Files   []xmlFile    `xml:"file"`
}

type xmlFolder struct {
	ID       int64  `xml:"id"`
	ParentID int64  `xml:"parentId"`
	Name     string `xml:"name"`
	CreateDate string `xml:"createDate"`
}

type xmlFile struct {
	ID       int64  `xml:"id"`
	ParentID int64  `xml:"parentId"`
	Name     string `xml:"name"`
	Size     int64  `xml:"size"`
	CreateDate string `xml:"createDate"`
	LastOpTime string `xml:"lastOpTime"`
	MD5      string `xml:"md5"`
}
