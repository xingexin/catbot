// Preserve numeric wire values while displaying the schema's human labels.
export function pluginSelectOptions(property: Record<string, unknown>) {
  if (Array.isArray(property.oneOf)) {
    const choices = property.oneOf;
    if (
      choices.length > 0 &&
      choices.every(
        (choice) =>
          choice &&
          typeof choice === "object" &&
          (typeof choice.const === "string" ||
            typeof choice.const === "number"),
      )
    ) {
      return choices.map((choice) => ({
        value: choice.const as string | number,
        label:
          typeof choice.title === "string"
            ? choice.title
            : String(choice.const),
      }));
    }
  }
  if (Array.isArray(property.enum)) {
    return property.enum.map((value) => ({ value, label: String(value) }));
  }
  return undefined;
}
