package zipextractor

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/itchio/savior/flatesource"
	"github.com/itchio/savior/seeksource"
	"github.com/itchio/wharf/state"

	"github.com/go-errors/errors"
	"github.com/itchio/arkive/zip"
	"github.com/itchio/savior"
)

const defaultFlateThreshold = 1 * 1024 * 1024

type ZipExtractor struct {
	source savior.Source
	zr     *zip.Reader

	reader io.ReaderAt

	saveConsumer savior.SaveConsumer
	consumer     *state.Consumer

	flateThreshold int64
}

var _ savior.Extractor = (*ZipExtractor)(nil)

func New(reader io.ReaderAt, readerSize int64) (*ZipExtractor, error) {
	zr, err := zip.NewReader(reader, readerSize)
	if err != nil {
		return nil, errors.Wrap(err, 0)
	}

	ex := &ZipExtractor{
		reader: reader,
		zr:     zr,

		saveConsumer: savior.NopSaveConsumer(),
		consumer:     savior.NopConsumer(),
	}
	return ex, nil
}

func (ze *ZipExtractor) SetSaveConsumer(saveConsumer savior.SaveConsumer) {
	ze.saveConsumer = saveConsumer
}

func (ze *ZipExtractor) SetConsumer(consumer *state.Consumer) {
	ze.consumer = consumer
}

func (ze *ZipExtractor) SetFlateThreshold(flateThreshold int64) {
	ze.flateThreshold = flateThreshold
}

func (ze *ZipExtractor) FlateThreshold() int64 {
	if ze.flateThreshold > 0 {
		return ze.flateThreshold
	}
	return defaultFlateThreshold
}

func (ze *ZipExtractor) Resume(checkpoint *savior.ExtractorCheckpoint, sink savior.Sink) (*savior.ExtractorResult, error) {
	zr := ze.zr

	isFresh := false

	if checkpoint == nil {
		isFresh = true
		ze.consumer.Infof("→ Starting fresh extraction")
		checkpoint = &savior.ExtractorCheckpoint{
			EntryIndex: 0,
		}
	} else {
		ze.consumer.Infof("↻ Resuming @ %.1f%%", checkpoint.Progress*100)
	}

	numEntries := int64(len(zr.File))

	var doneBytes int64
	var totalBytes int64
	for i, zf := range zr.File {
		size := int64(zf.UncompressedSize64)
		totalBytes += size
		if int64(i) < checkpoint.EntryIndex {
			doneBytes += size
		}
	}

	if isFresh {
		ze.consumer.Infof("⇓ Pre-allocating %s on disk", humanize.IBytes(uint64(totalBytes)))
		preallocateStart := time.Now()
		for _, zf := range zr.File {
			entry := zipFileEntry(zf)
			if entry.Kind == savior.EntryKindFile {
				err := sink.Preallocate(entry)
				if err != nil {
					return nil, errors.Wrap(err, 0)
				}
			}
		}
		preallocateDuration := time.Since(preallocateStart)
		ze.consumer.Infof("⇒ Pre-allocated in %s, nothing can stop us now", preallocateDuration)
	}

	var stopError error

	// allocate a copy buffer once
	copier := savior.NewCopier(ze.saveConsumer)

	for entryIndex := checkpoint.EntryIndex; entryIndex < numEntries && stopError == nil; entryIndex++ {
		savior.Debugf(`doing entryIndex %d`, entryIndex)
		zf := zr.File[entryIndex]

		err := func() error {
			checkpoint.EntryIndex = entryIndex

			if checkpoint.Entry == nil {
				checkpoint.Entry = zipFileEntry(zf)
			}
			entry := checkpoint.Entry

			ze.consumer.Debugf("→ %s", entry)

			switch entry.Kind {
			case savior.EntryKindDir:
				err := sink.Mkdir(entry)
				if err != nil {
					return errors.Wrap(err, 0)
				}
			case savior.EntryKindSymlink:
				rc, err := zf.Open()
				if err != nil {
					return errors.Wrap(err, 0)
				}

				defer rc.Close()

				linkname, err := ioutil.ReadAll(rc)
				if err != nil {
					return errors.Wrap(err, 0)
				}

				err = sink.Symlink(entry, string(linkname))
				if err != nil {
					return errors.Wrap(err, 0)
				}
			case savior.EntryKindFile:
				var src savior.Source

				switch zf.Method {
				case zip.Store, zip.Deflate:
					dataOff, err := zf.DataOffset()
					if err != nil {
						return errors.Wrap(err, 0)
					}

					compressedSize := int64(zf.CompressedSize64)

					reader := io.NewSectionReader(ze.reader, dataOff, compressedSize)
					rawSource := seeksource.NewWithSize(reader, compressedSize)

					switch zf.Method {
					case zip.Store:
						src = rawSource
					case zip.Deflate:
						src = flatesource.New(rawSource)
					}
				default:
					// will have to copy
				}

				if src == nil {
					// save/resume not supported for this storage format
					// (probably LZMA), doing a simple copy
					entry.WriteOffset = 0

					rc, err := zf.Open()
					if err != nil {
						return errors.Wrap(err, 0)
					}

					defer rc.Close()

					writer, err := sink.GetWriter(entry)
					if err != nil {
						return errors.Wrap(err, 0)
					}

					_, err = io.Copy(writer, rc)
					if err != nil {
						return errors.Wrap(err, 0)
					}
				} else {
					offset, err := src.Resume(checkpoint.SourceCheckpoint)
					if err != nil {
						return errors.Wrap(err, 0)
					}

					if offset < entry.WriteOffset {
						delta := entry.WriteOffset - offset
						savior.Debugf(`%s: discarding %d bytes to align source and writer`, entry.CanonicalPath, delta)
						savior.Debugf(`%s: (source resumed at %d, writer was at %d)`, entry.CanonicalPath, offset, entry.WriteOffset)
						err := savior.DiscardByRead(src, delta)
						if err != nil {
							return errors.Wrap(err, 0)
						}
					}
					savior.Debugf(`%s: zipextractor resuming from %s`, entry.CanonicalPath, humanize.IBytes(uint64(entry.WriteOffset)))

					writer, err := sink.GetWriter(entry)
					if err != nil {
						return errors.Wrap(err, 0)
					}

					computeProgress := func() float64 {
						actualDoneBytes := doneBytes + entry.WriteOffset
						return float64(actualDoneBytes) / float64(totalBytes)
					}

					src.SetSourceSaveConsumer(&savior.CallbackSourceSaveConsumer{
						OnSave: func(sourceCheckpoint *savior.SourceCheckpoint) error {
							savior.Debugf(`%s: saving, has source checkpoint? %v`, entry.CanonicalPath, sourceCheckpoint != nil)
							if sourceCheckpoint != nil {
								savior.Debugf(`%s: source checkpoint is at %d`, entry.CanonicalPath, sourceCheckpoint.Offset)
							}
							checkpoint.SourceCheckpoint = sourceCheckpoint

							err = writer.Sync()
							if err != nil {
								return errors.Wrap(err, 0)
							}

							checkpoint.Progress = computeProgress()

							action, err := ze.saveConsumer.Save(checkpoint)
							if err != nil {
								return errors.Wrap(err, 0)
							}
							if action == savior.AfterSaveStop {
								copier.Stop()
								stopError = savior.ErrStop
							}

							return nil
						},
					})

					err = copier.Do(&savior.CopyParams{
						Src:   src,
						Dst:   writer,
						Entry: entry,

						Savable: src,

						EmitProgress: func() {
							ze.consumer.Progress(computeProgress())
						},
					})
					if err != nil {
						return errors.Wrap(err, 0)
					}
				}
			}
			doneBytes += int64(zf.UncompressedSize64)

			return nil
		}()
		if err != nil {
			return nil, errors.Wrap(err, 0)
		}

		checkpoint.SourceCheckpoint = nil
		checkpoint.Entry = nil
	}

	if stopError != nil {
		return nil, savior.ErrStop
	}

	res := &savior.ExtractorResult{}
	for _, zf := range zr.File {
		res.Entries = append(res.Entries, zipFileEntry(zf))
	}

	ze.consumer.Statf("Extracted %s", res.Stats())

	return res, nil
}

func (ze *ZipExtractor) Features() savior.ExtractorFeatures {
	// zip has great resume support and is random access!
	return savior.ExtractorFeatures{
		Name:          "zip",
		ResumeSupport: savior.ResumeSupportBlock,
		Preallocate:   true,
		RandomAccess:  true,
	}
}

func zipFileEntry(zf *zip.File) *savior.Entry {
	entry := &savior.Entry{
		CanonicalPath:    filepath.ToSlash(zf.Name),
		CompressedSize:   int64(zf.CompressedSize64),
		UncompressedSize: int64(zf.UncompressedSize64),
		Mode:             zf.Mode(),
	}

	info := zf.FileInfo()

	if info.IsDir() {
		entry.Kind = savior.EntryKindDir
	} else if entry.Mode&os.ModeSymlink > 0 {
		entry.Kind = savior.EntryKindSymlink
	} else {
		entry.Kind = savior.EntryKindFile
	}
	return entry
}
