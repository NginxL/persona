/** Extension icon names never become resource paths supplied by an extension. */
export function extensionIcon(name: string): Resource {
  if (name === 'list') return $r('sys.symbol.list_bullet');
  if (name === 'bell') return $r('sys.symbol.bell');
  if (name === 'calendar') return $r('sys.symbol.calendar');
  if (name === 'doc') return $r('sys.symbol.doc_plaintext');
  if (name === 'gauge' || name === 'chart') return $r('sys.symbol.gauge_with_dots_needle_33percent');
  if (name === 'merge') return $r('sys.symbol.arrow_triangle_merge');
  return $r('sys.symbol.square_grid_2x2');
}
