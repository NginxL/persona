// Only an open model editor explains a typed ID. The launcher and store share the validation rule before the editor opens.
import { t } from "../i18n";
import { cleanModelId, MAX_MODEL_ID_LEN, modelIdProblem, type ModelIdProblem } from "./model-id";

/** The reason under the field. */
export function modelIdProblemText(p: ModelIdProblem): string {
  switch (p.kind) {
    case "empty":
      return t("lib-model-id.empty");
    case "start":
      return t("lib-model-id.start");
    case "space":
      return t("lib-model-id.space");
    case "control":
      return t("lib-model-id.control");
    case "character":
      return t("lib-model-id.character", { char: p.char });
    case "too_long":
      return t("lib-model-id.too_long", { max: MAX_MODEL_ID_LEN });
  }
}

/** Why what is typed can't be used, "" while it is empty or fine. */
export function typedModelProblem(text: string): string {
  const id = cleanModelId(text);
  if (!id) return "";
  const p = modelIdProblem(id);
  return p ? modelIdProblemText(p) : "";
}

/** The rule as one line, under a field that is still empty or fine. */
export const modelIdRule = () => t("lib-model-id.rule");
