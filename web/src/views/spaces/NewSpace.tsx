// New space (`/tracks/new`, #13500, docs/web/web.md "Domain spaces"): a name, a short line about it, an icon, which kinds
// of personal information it holds, and (folded under More options) its kind and its address. Created, it opens.
import { useEffect, useId, useMemo, useState } from "react";
import { errorText } from "../../lib/api";
import { navigate } from "../../lib/router";
import { createTrack, DEFAULT_KIND, formSlug, INFO_CATEGORIES, listTrackKinds, noteTrack, SPACE_ICONS, suggestSlug, useTracks, type TrackKind } from "../../lib/tracks";
import { Button, cx, EASE, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { categoryLabel } from "./labels";
import { t } from "../../i18n";

/** The kinds a new space may be of: enabled ones, the main chat's own way of working left out (it is not a place). */
export function spaceKinds(kinds: readonly TrackKind[]): TrackKind[] {
  return kinds.filter((k) => !k.disabled && k.slug !== "orchestration");
}

const FIELD = "w-full rounded-control bg-surface px-3 py-2 text-meta text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-btn";

export function NewSpace({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  const { tracks } = useTracks();
  const taken = useMemo(() => (tracks ?? []).map((x) => x.slug), [tracks]);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [icon, setIcon] = useState<string>(SPACE_ICONS[0]!);
  const [categories, setCategories] = useState<string[]>([]);
  const [kinds, setKinds] = useState<TrackKind[]>([]);
  const [kind, setKind] = useState(DEFAULT_KIND);
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ids = { title: useId(), description: useId(), slug: useId(), kind: useId() };

  useEffect(() => {
    let live = true;
    listTrackKinds().then((k) => {
      if (!live) return;
      const loaded = spaceKinds(k);
      setKinds(loaded);
      // the default kind may be off or missing on this assistant: start on one it offers
      setKind((cur) => (loaded.length && !loaded.some((x) => x.slug === cur) ? loaded[0]!.slug : cur));
    }, () => {});
    return () => {
      live = false;
    };
  }, []);

  // the address follows the name until the user writes one of their own
  const shownSlug = slugEdited ? slug : title.trim() ? suggestSlug(title, taken) : "";
  const slugOk = formSlug(shownSlug) && !taken.includes(shownSlug);
  const ready = title.trim() !== "" && slugOk && !busy;

  const create = async () => {
    if (!ready) return;
    setBusy(true);
    setError("");
    try {
      const track = await createTrack({ slug: shownSlug, kind, title: title.trim(), icon, description: description.trim(), default_categories: categories });
      noteTrack(track);
      navigate({ name: "track", slug: track.slug, fresh: true }, true);
    } catch (e) {
      setError(t("spaces.create_failed", { error: errorText(e) }));
    } finally {
      setBusy(false);
    }
  };

  const toggleCategory = (c: string) => setCategories((x) => (x.includes(c) ? x.filter((y) => y !== c) : [...x, c]));

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("spaces.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="min-w-0 flex-1 truncate text-card-title font-semibold text-ink">{t("spaces.new_title")}</h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <form
          className="mx-auto flex max-w-[600px] flex-col gap-5 px-4 py-6 sm:px-8"
          style={{ animation: `fade-up 320ms ${EASE} both` }}
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <p className="text-meta leading-relaxed text-ink-2">{t("spaces.new_intro")}</p>

          <div className="flex flex-col gap-1.5">
            <label htmlFor={ids.title} className="text-label font-medium text-ink-2">{t("spaces.field_title")}</label>
            <input id={ids.title} value={title} onChange={(e) => setTitle(e.target.value)} maxLength={80} autoFocus placeholder={t("spaces.field_title_placeholder")} className={FIELD} />
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor={ids.description} className="text-label font-medium text-ink-2">{t("spaces.field_description")}</label>
            <input id={ids.description} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={200} placeholder={t("spaces.field_description_placeholder")} className={FIELD} />
          </div>

          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-label font-medium text-ink-2">{t("spaces.field_icon")}</legend>
            <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label={t("spaces.field_icon")}>
              {SPACE_ICONS.map((name) => (
                <button
                  key={name}
                  type="button"
                  role="radio"
                  aria-checked={icon === name}
                  aria-label={t(`spaces.icon_${name}`)}
                  title={t(`spaces.icon_${name}`)}
                  onClick={() => setIcon(name)}
                  className={cx("touch-hit flex size-9 items-center justify-center rounded-control transition-colors", icon === name ? "bg-accent-tint text-accent-ink shadow-btn" : "bg-surface text-ink-2 shadow-hairline hover:bg-hover")}
                >
                  <Icon name={name} size={16} />
                </button>
              ))}
            </div>
          </fieldset>

          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-label font-medium text-ink-2">{t("spaces.field_categories")}</legend>
            <div className="flex flex-wrap gap-1.5">
              {INFO_CATEGORIES.map((c) => (
                <button
                  key={c}
                  type="button"
                  aria-pressed={categories.includes(c)}
                  onClick={() => toggleCategory(c)}
                  className={cx("inline-flex h-7 items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-label font-medium transition-colors", categories.includes(c) ? "bg-accent-tint text-accent-ink" : "bg-field text-ink-2 hover:bg-hover")}
                >
                  {categories.includes(c) && <Icon name="check" size={11} stroke={2.4} />}
                  {categoryLabel(c)}
                </button>
              ))}
            </div>
            <p className="text-label leading-relaxed text-ink-3">{t("spaces.field_categories_hint")}</p>
          </fieldset>

          <div>
            <Button tone="quiet" icon={more ? "chevron" : "chevronRight"} expanded={more} onClick={() => setMore((x) => !x)}>{t("spaces.more_options")}</Button>
            {more && (
              <div className="mt-3 flex flex-col gap-4 rounded-card bg-surface p-4 shadow-card">
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={ids.kind} className="text-label font-medium text-ink-2">{t("spaces.field_kind")}</label>
                  <select id={ids.kind} value={kind} onChange={(e) => setKind(e.target.value)} className={FIELD}>
                    {(kinds.length ? kinds : [{ slug: DEFAULT_KIND, title: "" } as TrackKind]).map((k) => (
                      <option key={k.slug} value={k.slug}>{k.slug === DEFAULT_KIND ? t("spaces.kind_space") : k.title || k.slug}</option>
                    ))}
                  </select>
                </div>
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={ids.slug} className="text-label font-medium text-ink-2">{t("spaces.field_slug")}</label>
                  <input
                    id={ids.slug}
                    value={shownSlug}
                    onChange={(e) => {
                      setSlugEdited(true);
                      setSlug(e.target.value.toLowerCase());
                    }}
                    maxLength={32}
                    spellCheck={false}
                    className={cx(FIELD, "font-mono")}
                  />
                  <p className={cx("text-label leading-relaxed", shownSlug && !slugOk ? "text-red" : "text-ink-3")}>
                    {shownSlug && taken.includes(shownSlug) ? t("spaces.field_slug_taken") : t("spaces.field_slug_hint")}
                  </p>
                </div>
              </div>
            )}
          </div>

          {error && <p role="alert" className="rounded-control bg-orange-tint px-3 py-2 text-meta text-ink">{error}</p>}

          <div className="flex justify-end gap-2">
            <Button tone="quiet" onClick={() => navigate({ name: "persona" })}>{t("spaces.cancel")}</Button>
            <Button tone="primary" type="submit" icon="plus" busy={busy} disabled={!ready}>{t("spaces.create")}</Button>
          </div>
        </form>
      </div>
    </div>
  );
}
