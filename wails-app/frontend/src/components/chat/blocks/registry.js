import { validateChoices } from './choicesSchema.js'
import { ChoicesBlock } from './ChoicesBlock.jsx'

// One entry per block type: { validate(json) -> data (throws Bad), Component }.
// Each block module adds itself here.
export const BLOCKS = {
  choices: { validate: validateChoices, Component: ChoicesBlock },
}
