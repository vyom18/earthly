package earthfile2llb

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/earthly/earthly/util/inodeutil"
	"github.com/earthly/earthly/util/llbutil/llbfactory"
)

func getSharedKeyHintFromInclude(name string, incl []string) string {
	h := sha1.New()
	b := make([]byte, 8)

	addToHash := func(path string) {
		h.Write([]byte(path))
		inode := inodeutil.GetInodeBestEffort(path)
		binary.LittleEndian.PutUint64(b, inode)
		h.Write(b)
	}

	addToHash(name)
	for _, path := range incl {
		addToHash(path)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func createIncludePatterns(incl []string) []string {
	incl2 := []string{}
	for _, inc := range incl {
		if inc == "." {
			inc = "./*"
		} else if strings.HasSuffix(inc, "/.") {
			inc = inc[:len(inc)-1] + "*"
		}
		incl2 = append(incl2, inc)
	}
	return incl2
}

func addIncludePathAndSharedKeyHint(factory llbfactory.Factory, src []string) llbfactory.Factory {
	localFactory, ok := factory.(*llbfactory.LocalFactory)
	if !ok {
		return factory
	}

	includePatterns := createIncludePatterns(src)
	sharedKey := getSharedKeyHintFromInclude(localFactory.GetName(), includePatterns)

	return localFactory.
		WithInclude(includePatterns).
		WithSharedKeyHint(sharedKey)
}
