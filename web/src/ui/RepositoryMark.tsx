// The repository providers' own marks, used as they come (CLAUDE.md: brand marks are not redrawn): GitHub's from
// Primer's Octicons as a template (one color, drawn in the text color so it reads in light and dark), GitLab's the
// Simple Icons trace (CC0) in GitLab's orange. The names and marks belong to their owners; they only say which
// provider a saved repository lives on. An extension's source has no mark of its own here: it gets the extension
// glyph (the one extensions are drawn with everywhere) in the secondary text color, and its name is said in text
// beside it.
import githubMark from "./brands/github.svg";
import gitlabMark from "./brands/gitlab.svg";
import { Icon } from "./icons";

/** `size` is the box the mark is drawn in, the same small scale as the stroke icons beside it. */
export function RepositoryMark({ provider, size = 14, className = "" }: { provider: string; size?: number; className?: string }) {
  if (provider === "github") {
    const mask = `url("${githubMark}") center / contain no-repeat`;
    return <span aria-hidden data-repository-mark="github" className={`inline-block shrink-0 bg-ink ${className}`}
      style={{ width: size, height: size, mask, WebkitMask: mask }} />;
  }
  if (provider === "gitlab") {
    return <img src={gitlabMark} alt="" aria-hidden data-repository-mark="gitlab" width={size} height={size}
      className={`shrink-0 ${className}`} style={{ width: size, height: size }} />;
  }
  if (provider.startsWith("ext:")) {
    return <span aria-hidden data-repository-mark="extension" className={`inline-flex shrink-0 text-ink-2 ${className}`}>
      <Icon name="extension" size={size} />
    </span>;
  }
  return null;
}
