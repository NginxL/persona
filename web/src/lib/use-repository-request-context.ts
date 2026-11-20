import { useEffect, useMemo, useRef } from "react";
import { repositoryRequestContext } from "./task-repositories";

/** The shared owner and lifetime fence for repository metadata and naming requests. */
export function useRepositoryRequestContext(manager: string, owner: string) {
  const alive = useRef(true);
  const identity = useRef("");
  identity.current = `${manager}:${owner}`;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  return useMemo(() => repositoryRequestContext(manager, owner, () => alive.current && identity.current === `${manager}:${owner}`), [manager, owner]);
}
