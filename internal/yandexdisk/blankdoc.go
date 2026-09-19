package yandexdisk

import (
	"archive/zip"
	"bytes"
)

// blankDocx builds a minimal, valid, empty .docx in memory: just the parts
// Word/OOXML-compatible editors require (content types, the package
// relationship to the main document part, and one empty paragraph). No
// external file or dependency needed for something this small.
func blankDocx() []byte {
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p/>
    <w:sectPr/>
  </w:body>
</w:document>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Deterministic order (map iteration isn't) -- doesn't matter for a
	// zip's validity, just makes output reproducible for tests.
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		f, err := zw.Create(name)
		if err != nil {
			panic("yandexdisk: build blank docx: " + err.Error())
		}
		if _, err := f.Write([]byte(files[name])); err != nil {
			panic("yandexdisk: build blank docx: " + err.Error())
		}
	}
	if err := zw.Close(); err != nil {
		panic("yandexdisk: build blank docx: " + err.Error())
	}
	return buf.Bytes()
}
