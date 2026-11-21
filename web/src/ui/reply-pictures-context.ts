// What a Markdown block tells its runs of pictures (ReplyImages.tsx): its text (whose pictures the viewer pages through)
// and whether they wait for a tap (`ask`: what other people wrote, where a picture may be a tracking pixel).
// First-screen code: only a context, the runs themselves load with a reply's first picture.
import { createContext } from "react";

export interface ReplyPictures {
  text: string;
  ask: boolean;
  mid?: string;
  cwd?: string;
}

export const ReplyPicturesContext = createContext<ReplyPictures>({ text: "", ask: false });
