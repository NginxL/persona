const SCHEMA_MAPS = ['properties', 'patternProperties', '$defs', 'definitions', 'dependentSchemas', 'dependencies']
const SINGLE_SCHEMAS = [
  'additionalProperties', 'additionalItems', 'contains', 'propertyNames', 'not', 'if', 'then', 'else',
  'unevaluatedProperties', 'unevaluatedItems', 'contentSchema',
]
const SCHEMA_ARRAYS = ['allOf', 'anyOf', 'oneOf', 'prefixItems']
const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value)

// agy's Gemini declarations accept only nonempty string enums (#9944). Project the declaration, not call arguments
// or the server's validator. Schema maps can have a property named "enum"; defaults/examples/const are data.
export function agyInputSchema(schema) {
  if (!isObject(schema)) return schema
  const projected = { ...schema }
  if (Object.hasOwn(schema, 'enum') && !(Array.isArray(schema.enum) && schema.enum.length > 0
    && schema.enum.every((value) => typeof value === 'string' && value.length > 0))) delete projected.enum
  for (const key of SCHEMA_MAPS) {
    if (isObject(schema[key])) {
      projected[key] = Object.fromEntries(Object.entries(schema[key]).map(([name, child]) => [name, agyInputSchema(child)]))
    }
  }
  for (const key of SINGLE_SCHEMAS) {
    if (Object.hasOwn(schema, key)) projected[key] = agyInputSchema(schema[key])
  }
  for (const key of SCHEMA_ARRAYS) {
    if (Array.isArray(schema[key])) projected[key] = schema[key].map(agyInputSchema)
  }
  if (Object.hasOwn(schema, 'items')) {
    projected.items = Array.isArray(schema.items) ? schema.items.map(agyInputSchema) : agyInputSchema(schema.items)
  }
  return projected
}
