package proxy

import (
	"compress/gzip"
	"compress/zlib"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// DecodedResponseAnalyzer 把转发路径和分析路径分开：压缩字节照常发给客户端，
// 分析器则边解压边喂给采集器。这样既不改 Content-Encoding / Content-Length，
// 又不会因为上游压缩而漏掉 usage。
type DecodedResponseAnalyzer struct {
	collector *ResponseBodyCollector
	writer    *io.PipeWriter
	done      <-chan error
}

func NewDecodedResponseAnalyzer(collector *ResponseBodyCollector, contentEncoding string) *DecodedResponseAnalyzer {
	encoding := strings.ToLower(strings.TrimSpace(contentEncoding))
	if encoding == "" || encoding == "identity" {
		return &DecodedResponseAnalyzer{collector: collector}
	}

	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		defer close(done)
		var decoded io.Reader
		switch encoding {
		case "gzip", "x-gzip":
			gzipReader, err := gzip.NewReader(reader)
			if err != nil {
				_, _ = io.Copy(io.Discard, reader)
				done <- err
				return
			}
			defer gzipReader.Close()
			decoded = gzipReader
		case "deflate":
			zlibReader, err := zlib.NewReader(reader)
			if err != nil {
				_, _ = io.Copy(io.Discard, reader)
				done <- err
				return
			}
			defer zlibReader.Close()
			decoded = zlibReader
		case "zstd":
			zstdReader, err := zstd.NewReader(reader,
				zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
			if err != nil {
				_, _ = io.Copy(io.Discard, reader)
				done <- err
				return
			}
			defer zstdReader.Close()
			decoded = zstdReader
		default:
			// 不认识的编码仍要排空管道，否则转发会被分析路径阻塞。
			_, err := io.Copy(io.Discard, reader)
			done <- err
			return
		}

		buffer := make([]byte, 32*1024)
		for {
			n, err := decoded.Read(buffer)
			if n > 0 {
				collector.Append(buffer[:n])
			}
			if err == io.EOF {
				done <- nil
				return
			}
			if err != nil {
				_, _ = io.Copy(io.Discard, reader)
				done <- err
				return
			}
		}
	}()
	return &DecodedResponseAnalyzer{collector: collector, writer: writer, done: done}
}

func (a *DecodedResponseAnalyzer) Append(chunk []byte) {
	if a.writer == nil {
		a.collector.Append(chunk)
		return
	}
	_, _ = a.writer.Write(chunk)
}

func (a *DecodedResponseAnalyzer) Close() {
	if a.writer == nil {
		return
	}
	_ = a.writer.Close()
	<-a.done
	a.writer = nil
}
