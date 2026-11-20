// The shared synthetic configuration documents (schema/configuration): the same JSON the Go checks, the schema tests and
// every client read, not a Web copy. Tests and the e2e fixture page (e2e/configuration-form.html) take them from here.
import allTypes from "../../../../../schema/configuration/all-types.json";
import validationCases from "../../../../../schema/configuration/validation-cases.json";

export { allTypes, validationCases };
