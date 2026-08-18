/**
 * Client-side codec support detection via MediaSource.isTypeSupported().
 */

export interface CodecSupport {
  h264: boolean;
  hevc: boolean;
  av1: boolean;
  vp9: boolean;
  aac: boolean;
  ac3: boolean;
  eac3: boolean;
  opus: boolean;
}

export function codecProbe(): CodecSupport {
  const check = (mime: string): boolean => {
    try {
      return (
        MediaSource.isTypeSupported(mime) ||
        document.createElement('video').canPlayType(mime) !== ''
      );
    } catch {
      return false;
    }
  };

  return {
    h264: check('video/mp4; codecs="avc1.640028"'),
    hevc:
      check('video/mp4; codecs="hev1.1.6.L93.B0"') ||
      check('video/mp4; codecs="hvc1.1.6.L93.B0"'),
    av1: check('video/mp4; codecs="av01.0.08M.08"'),
    vp9: check('video/webm; codecs="vp9"') || check('video/mp4; codecs="vp09.00.10.08"'),
    aac: check('audio/mp4; codecs="mp4a.40.2"'),
    ac3: check('audio/mp4; codecs="ac-3"'),
    eac3: check('audio/mp4; codecs="ec-3"'),
    opus: check('audio/webm; codecs="opus"') || check('audio/mp4; codecs="opus"'),
  };
}
