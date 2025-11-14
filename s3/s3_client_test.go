package s3x

import (
	"encoding/base64"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/minio/minio/cmd"

	"github.com/dustin/go-humanize"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var minioClient *minio.Client

func initClient() {
	endpoint := "172.27.116.110:9000"
	accessKey := "minioadmin"
	secretKey := "minioadmin"
	useSSL := false
	var err error
	minioClient, err = minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Fatal(err)
	}
}

func TestPutObject(t *testing.T) {
	initClient()
	fs, err := os.Stat("../file1")
	if err != nil {
		t.Fatal(err)
	}

	localfile, err := os.Open("../file1")
	if err != nil {
		t.Fatal(err)
	}

	var (
		bucket       = "test"
		object       = fs.Name()
		parityDrives = 0
		// writeQuorum  = 1
		dataDrives = 1
		diskNumber = 1
		actualSize = fs.Size()
		modTime    = time.Now().UTC()
	)

	fi := cmd.NewFileInfo(cmd.PathJoin(bucket, object), dataDrives, parityDrives)
	if fi.VersionID == "" {
		fi.VersionID = cmd.MustGetUUID()
	}

	fi.DataDir = cmd.MustGetUUID()
	fi.ModTime = time.Now()

	erasure, err := cmd.NewErasure(t.Context(), fi.Erasure.DataBlocks, fi.Erasure.ParityBlocks, fi.Erasure.BlockSize)
	if err != nil {
		log.Fatal(err)
	}

	buffer := make([]byte, humanize.MiByte)
	// switch size := fs.Size(); {
	// case size == 0:
	// 	buffer = make([]byte, 1)
	// case size >= fi.Erasure.BlockSize || size == -1:
	// 	buffer = globalBytePoolCap.Load().Get()
	// 	defer globalBytePoolCap.Load().Put(buffer)
	// case size < fi.Erasure.BlockSize:
	// 	buffer = make([]byte, size, 2*size+int64(fi.Erasure.ParityBlocks+fi.Erasure.DataBlocks-1))
	// }
	// if len(buffer) > int(fi.Erasure.BlockSize) {
	// 	buffer = buffer[:fi.Erasure.BlockSize]
	// }
	tempFile := cmd.MustGetUUID()
	writers := make([]io.Writer, diskNumber)
	files := make([]string, diskNumber)
	for i := range diskNumber {
		// 创建文件
		f, err := os.CreateTemp(os.TempDir(), tempFile+".")
		if err != nil {
			log.Fatal(err)
		}
		files[i] = f.Name()

		// writers[i] = cmd.NewStreals -mBitrotWriter(f, cmd.DefaultBitrotAlgorithm, erasure.ShardSize())
		writers[i] = f
	}

	n, erasureErr := erasure.Encode(t.Context(), localfile, writers, buffer, 1)
	if erasureErr != nil {
		log.Fatal(erasureErr)
	}
	for _, writer := range writers {
		if c, ok := writer.(io.Closer); ok {
			if err := c.Close(); err != nil {
				log.Fatal(err)
			}
		}
	}

	userDefined := map[string]string{}
	fi.Versioned = false
	fi.Data = nil
	fi.Erasure.Index = 1
	fi.Metadata = userDefined
	fi.Size = n
	fi.ModTime = modTime
	for i := range writers {
		fi.AddObjectPart(i+1, "", n, actualSize, modTime, nil, nil)
	}

	// zipfile, err := os.CreateTemp(os.TempDir(), fmt.Sprintf("%s.zip.", tempFile))
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// defer zipfile.Close()
	// zipWriter := zip.NewWriter(zipfile)
	// zipWriter.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
	// 	return flate.NewWriter(out, flate.DefaultCompression)
	// })
	// defer zipWriter.Close()

	// for _, file := range files {
	// 	err := addFileToZip(zipWriter, file)
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}
	// }

	// if err := zipWriter.Close(); err != nil {
	// 	log.Fatal(err)
	// }

	// fs, err = os.Stat(zipfile.Name())
	// if err != nil {
	// 	t.Fatal(err)
	// }

	// f, err := os.Open(zipfile.Name())
	// if err != nil {
	// 	log.Fatal(err)
	// }

	var xl cmd.XlMetaV2
	if err := xl.AddVersion(fi); err != nil {
		log.Fatal(err)
	}
	xlMetaBytes, err := xl.AppendTo(nil)
	if err != nil {
		t.Fatal(err)
	}

	xlMetaEncoded := base64.StdEncoding.EncodeToString(xlMetaBytes)

	if _, err := minioClient.FPutObject(t.Context(), bucket, object, files[0], minio.PutObjectOptions{
		UserMetadata: map[string]string{
			"XL-Meta": xlMetaEncoded,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDownload(t *testing.T) {
	initClient()
	bucket := "test"
	object := "file1"
	err := minioClient.FGetObject(t.Context(), bucket, object, "./downloadfile", minio.GetObjectOptions{})
	if err != nil {
		log.Fatal(err)
	}
}

func TestUnmarshal(t *testing.T) {
	var fi cmd.FileInfo
	b, err := os.ReadFile("../xl.meta")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fi.UnmarshalMsg(b); err != nil {
		t.Fatal(err)
	}
	t.Log(fi)
}
