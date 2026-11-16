import { validateChoices } from './choicesSchema.js'
import { ChoicesBlock } from './ChoicesBlock.jsx'
import { validateChart } from './chartSchema.js'
import { ChartBlock } from './ChartBlock.jsx'
import { validateTable } from './tableSchema.js'
import { TableBlock } from './TableBlock.jsx'

// One entry per block type: { validate(json) -> data (throws Bad), Component }.
// Each block module adds itself here.
export const BLOCKS = {
  choices: { validate: validateChoices, Component: ChoicesBlock },
  chart: { validate: validateChart, Component: ChartBlock },
  table: { validate: validateTable, Component: TableBlock },
}
