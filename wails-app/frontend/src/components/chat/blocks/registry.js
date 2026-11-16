import { validateChoices } from './choicesSchema.js'
import { ChoicesBlock } from './ChoicesBlock.jsx'
import { validateChart } from './chartSchema.js'
import { ChartBlock } from './ChartBlock.jsx'

// One entry per block type: { validate(json) -> data (throws Bad), Component }.
// Each block module adds itself here.
export const BLOCKS = {
  choices: { validate: validateChoices, Component: ChoicesBlock },
  chart: { validate: validateChart, Component: ChartBlock },
}
