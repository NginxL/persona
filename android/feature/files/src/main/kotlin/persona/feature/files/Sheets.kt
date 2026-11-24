package persona.feature.files

import androidx.annotation.StringRes
import org.xml.sax.Attributes
import org.xml.sax.InputSource
import org.xml.sax.helpers.DefaultHandler
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.time.LocalDateTime
import java.util.Locale
import java.util.zip.ZipInputStream
import javax.xml.parsers.SAXParserFactory
import kotlin.math.abs
import kotlin.math.roundToLong

/**
 * One sheet of a table file (docs/android/android.md "Files and Git in chat"): its cells as text, row by row; rows can be shorter than
 * [columns]. The same model as iOS (`TableParsing.swift`) and the web (`lib/sheets.ts`).
 */
internal class TableSheet(val name: String) {
    val rows = ArrayList<List<String>>()
    var columns = 0
        private set
    /** Rows past [MAX_ROWS] were left out. */
    var capped = false
        private set

    fun append(more: List<List<String>>) {
        val room = MAX_ROWS - rows.size
        if (more.size > room) capped = true
        for (row in more.take(room.coerceAtLeast(0))) {
            rows.add(row)
            columns = maxOf(columns, row.size)
        }
    }

    operator fun get(row: Int, column: Int): String = rows.getOrNull(row)?.getOrNull(column) ?: ""

    /** A row as tab-separated text (what "Copy row" puts on the clipboard). */
    fun rowText(row: Int): String = rows.getOrNull(row).orEmpty().joinToString("\t")

    companion object {
        /** More rows than this are not kept (a sheet this long is not read on a phone). */
        const val MAX_ROWS = 200_000

        /** The spreadsheet name of a column: A … Z, AA … */
        fun columnName(index: Int): String {
            val name = StringBuilder()
            var n = index + 1
            while (n > 0) {
                name.insert(0, ('A' + (n - 1) % 26))
                n = (n - 1) / 26
            }
            return name.toString()
        }
    }
}

/** Which files the table viewer opens: CSV / TSV as text, xlsx as a workbook. Old .xls and Numbers stay described. */
internal object TableFiles {
    private val delimited = setOf("csv", "tsv", "tab")
    private val workbooks = setOf("xlsx", "xlsm")

    fun isDelimited(name: String) = FilesPaths.extension(name) in delimited
    fun isTabSeparated(name: String) = FilesPaths.extension(name) in setOf("tsv", "tab")
    fun isWorkbook(name: String) = FilesPaths.extension(name) in workbooks
}

/**
 * CSV / TSV (RFC 4180: quoted fields with "" for a quote, separators and line breaks inside quotes). Blank lines are
 * skipped, a lone CR is ignored. Values stay exactly as written: no number or date guessing.
 */
internal object Delimited {
    /**
     * The separator for a file: tab for .tsv / .tab, else the commonest of comma, semicolon (what spreadsheets write
     * where the comma is the decimal mark) and tab on the first line; comma when there are none.
     */
    fun separator(name: String, text: String): Char {
        if (TableFiles.isTabSeparated(name)) return '\t'
        val first = text.take(4096).substringBefore('\n')
        return listOf(',', ';', '\t').maxBy { c -> first.count { it == c } }.takeIf { c -> first.contains(c) } ?: ','
    }

    fun parse(text: String, separator: Char, maxRows: Int = TableSheet.MAX_ROWS + 1): List<List<String>> {
        val out = ArrayList<List<String>>()
        val field = StringBuilder()
        var row = ArrayList<String>()
        var inQuotes = false
        var quotePending = false
        var quoted = false
        fun endField() { row.add(field.toString()); field.setLength(0); quoted = false }
        fun endRow() { if (!(row.size == 1 && row[0].isEmpty())) out.add(row); row = ArrayList() }
        var i = if (text.startsWith('\uFEFF')) 1 else 0
        while (i < text.length && out.size < maxRows) {
            val c = text[i++]
            if (inQuotes) {
                if (quotePending) {
                    quotePending = false
                    if (c == '"') { field.append('"'); continue }
                    inQuotes = false // the closing quote: this character is outside the quotes
                } else if (c == '"') { quotePending = true; continue } else { field.append(c); continue }
            }
            when {
                c == '"' && field.isEmpty() && !quoted -> { inQuotes = true; quoted = true }
                c == separator -> endField()
                c == '\n' -> { endField(); endRow() }
                c == '\r' -> Unit
                else -> field.append(c)
            }
        }
        if (out.size < maxRows && (field.isNotEmpty() || row.isNotEmpty() || quoted)) { endField(); endRow() }
        return out
    }

    /** A delimited file as the one sheet it is. */
    fun sheet(name: String, text: String): TableSheet =
        TableSheet(name.substringBeforeLast('.')).apply { append(parse(text, separator(name, text))) }
}

/** A workbook that can't be read: what to say. */
internal class SheetFailure(@StringRes val reason: Int) : Exception()

/**
 * An Excel workbook (.xlsx, Office Open XML): each sheet's cells as the text Excel would show for the plain cases:
 * shared and inline strings, numbers without float noise, TRUE / FALSE, errors, and dates for the date formats.
 */
internal object Xlsx {
    /** Bytes inflated per workbook at most (a zip bomb stops here instead of filling the phone's memory). */
    private const val MAX_INFLATED = 96L shl 20

    fun read(bytes: ByteArray): List<TableSheet> {
        val files = unzip(bytes)
        val workbook = files["xl/workbook.xml"] ?: throw SheetFailure(R.string.files_sheet_no_workbook)
        val sheets = Xml.elements(workbook, setOf("sheet"))
        val targets = files["xl/_rels/workbook.xml.rels"]?.let { Xml.elements(it, setOf("Relationship")) }.orEmpty()
            .mapNotNull { r ->
                val id = r["Id"] ?: return@mapNotNull null
                val target = r["Target"] ?: return@mapNotNull null
                id to normalizePath(if (target.startsWith("/")) target.drop(1) else "xl/$target")
            }.toMap()
        val strings = files["xl/sharedStrings.xml"]?.let(::sharedStrings).orEmpty()
        val dates = files["xl/styles.xml"]?.let(::dateStyles).orEmpty()
        val out = sheets.mapIndexedNotNull { i, s ->
            val name = s["name"] ?: "Sheet${i + 1}"
            val path = s["r:id"]?.let(targets::get) ?: "xl/worksheets/sheet${i + 1}.xml"
            files[path]?.let { cells(it, name, strings, dates) }
        }
        if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_no_sheets)
        return out
    }

    /** A part's name with "." and ".." resolved ("xl/../xl/worksheets/a.xml" → "xl/worksheets/a.xml"). */
    fun normalizePath(path: String): String {
        val parts = ArrayList<String>()
        for (seg in path.split('/')) when (seg) { "", "." -> Unit; ".." -> parts.removeLastOrNull(); else -> parts.add(seg) }
        return parts.joinToString("/")
    }

    /** The parts of an Office archive a reader uses (the XML under [folder]), inflated; the comparing reads word/ and ppt/. */
    internal fun unzip(bytes: ByteArray, folder: String = "xl/"): Map<String, ByteArray> {
        val out = HashMap<String, ByteArray>()
        var total = 0L
        try {
            ZipInputStream(ByteArrayInputStream(bytes)).use { zip ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val entry = zip.nextEntry ?: break
                    if (entry.isDirectory || !entry.name.startsWith(folder) || !entry.name.endsWith(".xml") && !entry.name.endsWith(".rels")) continue
                    val data = ByteArrayOutputStream()
                    while (true) {
                        val n = zip.read(buffer)
                        if (n < 0) break
                        total += n
                        if (total > MAX_INFLATED) throw SheetFailure(R.string.files_sheet_too_big)
                        data.write(buffer, 0, n)
                    }
                    out[entry.name] = data.toByteArray()
                }
            }
        } catch (e: SheetFailure) {
            throw e
        } catch (e: Exception) {
            if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_not_xlsx)
        }
        if (out.isEmpty()) throw SheetFailure(R.string.files_sheet_not_xlsx)
        return out
    }

    /** A number as Excel shows it in a General cell: integers without ".0", others to 15 significant digits. */
    fun number(raw: String): String {
        val v = raw.toDoubleOrNull()?.takeIf { it.isFinite() } ?: return raw
        if (v == Math.rint(v) && abs(v) < 1e15) return v.toLong().toString()
        var s = String.format(Locale.ROOT, "%.15g", v)
        if (s.contains('e')) {
            // %g writes e+05; Excel writes E+05 with the mantissa's trailing zeros dropped.
            val (m, e) = s.split('e')
            return m.trimEnd('0').trimEnd('.') + "E" + e
        }
        if (s.contains('.')) s = s.trimEnd('0').trimEnd('.')
        return s
    }

    /**
     * An Excel date serial (the 1900 system) as yyyy-MM-dd, with the time when it has one. Excel counts a 1900-02-29
     * that never was (serial 60, after Lotus 1-2-3): days from 61 on count from 1899-12-30, earlier ones from 12-31.
     */
    fun date(raw: String): String {
        val v = raw.toDoubleOrNull()?.takeIf { it.isFinite() && it > -1 && it < 2_958_466 } ?: return raw
        if (v >= 60 && v < 61) return if (v == 60.0) "1900-02-29" else "1900-02-29 " + time(LocalDateTime.of(2000, 1, 1, 0, 0).plusSeconds(((v - 60) * 86_400).roundToLong()))
        val seconds = (v * 86_400).roundToLong()
        val t = (if (v < 60) LocalDateTime.of(1899, 12, 31, 0, 0) else LocalDateTime.of(1899, 12, 30, 0, 0)).plusSeconds(seconds)
        val day = "%04d-%02d-%02d".format(Locale.ROOT, t.year, t.monthValue, t.dayOfMonth)
        if (seconds % 86_400 == 0L) return day
        return if (v < 1) time(t) else "$day ${time(t)}"
    }

    private fun time(t: LocalDateTime) =
        if (t.second == 0) "%02d:%02d".format(Locale.ROOT, t.hour, t.minute) else "%02d:%02d:%02d".format(Locale.ROOT, t.hour, t.minute, t.second)

    /** xl/sharedStrings.xml: each `<si>`'s text, its runs joined; phonetic guides (`<rPh>`) left out. */
    private fun sharedStrings(xml: ByteArray): List<String> {
        val strings = ArrayList<String>()
        val current = StringBuilder()
        var inText = false
        var phonetic = 0
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) { "si" -> current.setLength(0); "rPh" -> phonetic++; "t" -> inText = phonetic == 0 }
            }
            override fun endElement(uri: String?, localName: String?, qName: String) {
                when (Xml.local(qName)) { "si" -> strings.add(current.toString()); "rPh" -> phonetic--; "t" -> inText = false }
            }
            override fun characters(ch: CharArray, start: Int, length: Int) { if (inText) current.appendRange(ch, start, start + length) }
        })
        return strings
    }

    /** xl/styles.xml: which cell styles (`cellXfs` index) show a number as a date or time. */
    private fun dateStyles(xml: ByteArray): List<Boolean> {
        val custom = HashMap<Int, String>()
        val formats = ArrayList<Int>()
        var inCellXfs = false
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "numFmt" -> {
                        val id = attributes.getValue("numFmtId")?.toIntOrNull()
                        val code = attributes.getValue("formatCode")
                        if (id != null && code != null) custom[id] = code
                    }
                    "cellXfs" -> inCellXfs = true
                    "xf" -> if (inCellXfs) formats.add(attributes.getValue("numFmtId")?.toIntOrNull() ?: 0)
                }
            }
            override fun endElement(uri: String?, localName: String?, qName: String) { if (Xml.local(qName) == "cellXfs") inCellXfs = false }
        })
        return formats.map { id -> id in 14..22 || id in 45..47 || custom[id]?.let(::isDateCode) == true }
    }

    /** A custom number format is a date when it has date letters outside quotes and brackets ([Red], "text"). */
    private fun isDateCode(code: String): Boolean {
        val plain = StringBuilder()
        var skip: Char? = null
        for (ch in code.lowercase(Locale.ROOT)) {
            if (skip != null) { if (ch == skip) skip = null; continue }
            when (ch) { '"' -> skip = '"'; '[' -> skip = ']'; else -> plain.append(ch) }
        }
        return plain.any { it in "ymdhs" }
    }

    /** The column of a reference like "AB12" (0-based), or null. */
    fun column(ref: String): Int? {
        var n = 0
        var any = false
        for (c in ref) {
            if (c !in 'A'..'Z') break
            n = n * 26 + (c - 'A' + 1)
            any = true
        }
        return if (any) n - 1 else null
    }

    /** One worksheet's `<sheetData>`: cells placed by their reference (B3), gaps left empty. */
    private fun cells(xml: ByteArray, name: String, strings: List<String>, dates: List<Boolean>): TableSheet {
        val sheet = TableSheet(name)
        var row = ArrayList<String>()
        var rowIndex = -1
        var cellColumn = 0
        var cellType = ""
        var cellStyle = 0
        val value = StringBuilder()
        var inValue = false
        var inInline = false
        fun flushRow() {
            if (rowIndex < 0) return
            while (row.lastOrNull() == "") row.removeAt(row.size - 1)
            sheet.append(listOf(row))
            row = ArrayList()
        }
        Xml.parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                when (Xml.local(qName)) {
                    "row" -> {
                        if (sheet.capped) return
                        flushRow()
                        val r = attributes.getValue("r")?.toIntOrNull()?.minus(1) ?: (rowIndex + 1)
                        // Rows the file skips are empty rows (bounded: a row far down doesn't allocate the gap past the cap).
                        if (r > rowIndex + 1) sheet.append(List(minOf(r - rowIndex - 1, TableSheet.MAX_ROWS + 1)) { emptyList() })
                        rowIndex = r
                    }
                    "c" -> {
                        if (sheet.capped) return
                        cellColumn = attributes.getValue("r")?.let(::column) ?: row.size
                        cellType = attributes.getValue("t") ?: ""
                        cellStyle = attributes.getValue("s")?.toIntOrNull() ?: 0
                        value.setLength(0)
                    }
                    "v" -> inValue = true
                    "is" -> inInline = true
                    "t" -> if (inInline) inValue = true
                }
            }

            override fun endElement(uri: String?, localName: String?, qName: String) {
                when (Xml.local(qName)) {
                    "v", "t" -> inValue = false
                    "is" -> inInline = false
                    "c" -> {
                        if (sheet.capped) return
                        val raw = value.toString()
                        val shown = when (cellType) {
                            "s" -> raw.toIntOrNull()?.let(strings::getOrNull) ?: ""
                            "b" -> if (raw == "1") "TRUE" else "FALSE"
                            "str", "inlineStr", "e" -> raw
                            else -> when {
                                raw.isEmpty() -> ""
                                dates.getOrNull(cellStyle) == true -> date(raw)
                                else -> number(raw)
                            }
                        }
                        if (cellColumn >= 16_384) return
                        while (row.size <= cellColumn) row.add("")
                        row[cellColumn] = shown
                    }
                }
            }

            override fun characters(ch: CharArray, start: Int, length: Int) { if (inValue) value.appendRange(ch, start, start + length) }
        })
        flushRow()
        return sheet
    }
}

/** SAX over the workbook's XML, with DTDs and external entities off (a workbook is untrusted input). */
internal object Xml {
    fun local(qName: String) = qName.substringAfter(':')

    /**
     * Whether a part declares a DTD or entities. Workbook parts never do; Android's SAX (Expat) ignores the features
     * that would turn DTDs off, so such a part isn't parsed at all (no entity expansion to blow up).
     */
    fun declaresDtd(xml: ByteArray): Boolean {
        val markers = listOf("<!DOCTYPE", "<!ENTITY").map { it.toByteArray() }
        for (i in xml.indices) {
            if (xml[i] != '<'.code.toByte()) continue
            for (m in markers) if (i + m.size <= xml.size && (m.indices).all { k -> xml[i + k].toInt().toChar().uppercaseChar() == m[k].toInt().toChar() }) return true
        }
        return false
    }

    fun parse(xml: ByteArray, handler: DefaultHandler) {
        if (declaresDtd(xml)) return
        val factory = SAXParserFactory.newInstance().apply { isNamespaceAware = false }
        for ((feature, on) in listOf(
            "http://apache.org/xml/features/disallow-doctype-decl" to true,
            "http://xml.org/sax/features/external-general-entities" to false,
            "http://xml.org/sax/features/external-parameter-entities" to false,
        )) {
            val _ = runCatching { factory.setFeature(feature, on) }
        }
        try {
            factory.newSAXParser().parse(InputSource(ByteArrayInputStream(xml)), handler)
        } catch (_: Exception) {
            // A cut or broken part: keep what was read before the error.
        }
    }

    /** Every element with one of the names, as its attributes by qualified name (so `r:id`). */
    fun elements(xml: ByteArray, names: Set<String>): List<Map<String, String>> {
        val found = ArrayList<Map<String, String>>()
        parse(xml, object : DefaultHandler() {
            override fun startElement(uri: String?, localName: String?, qName: String, attributes: Attributes) {
                if (local(qName) in names) found.add((0 until attributes.length).associate { attributes.getQName(it) to attributes.getValue(it) })
            }
        })
        return found
    }
}
