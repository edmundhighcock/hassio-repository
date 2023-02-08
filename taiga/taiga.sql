--
-- PostgreSQL database dump
--

-- Dumped from database version 13.9
-- Dumped by pg_dump version 13.9

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: array_distinct(anyarray); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.array_distinct(anyarray) RETURNS anyarray
    LANGUAGE sql
    AS $_$
              SELECT ARRAY(SELECT DISTINCT unnest($1))
            $_$;


ALTER FUNCTION public.array_distinct(anyarray) OWNER TO taiga;

--
-- Name: clean_key_in_custom_attributes_values(); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.clean_key_in_custom_attributes_values() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
                       DECLARE
                               key text;
                               project_id int;
                               object_id int;
                               attribute text;
                               tablename text;
                               custom_attributes_tablename text;
                         BEGIN
                               key := OLD.id::text;
                               project_id := OLD.project_id;
                               attribute := TG_ARGV[0]::text;
                               tablename := TG_ARGV[1]::text;
                               custom_attributes_tablename := TG_ARGV[2]::text;

                               EXECUTE 'UPDATE ' || quote_ident(custom_attributes_tablename) || '
                                           SET attributes_values = json_object_delete_keys(attributes_values, ' || quote_literal(key) || ')
                                          FROM ' || quote_ident(tablename) || '
                                         WHERE ' || quote_ident(tablename) || '.project_id = ' || project_id || '
                                           AND ' || quote_ident(custom_attributes_tablename) || '.' || quote_ident(attribute) || ' = ' || quote_ident(tablename) || '.id';
                               RETURN NULL;
                           END; $$;


ALTER FUNCTION public.clean_key_in_custom_attributes_values() OWNER TO taiga;

--
-- Name: inmutable_array_to_string(text[]); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.inmutable_array_to_string(text[]) RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $_$SELECT array_to_string($1, ' ', '')$_$;


ALTER FUNCTION public.inmutable_array_to_string(text[]) OWNER TO taiga;

--
-- Name: json_object_delete_keys(json, text[]); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.json_object_delete_keys(json json, VARIADIC keys_to_delete text[]) RETURNS json
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
                   SELECT COALESCE ((SELECT ('{' || string_agg(to_json("key") || ':' || "value", ',') || '}')
                                       FROM json_each("json")
                                      WHERE "key" <> ALL ("keys_to_delete")),
                                    '{}')::json $$;


ALTER FUNCTION public.json_object_delete_keys(json json, VARIADIC keys_to_delete text[]) OWNER TO taiga;

--
-- Name: json_object_delete_keys(jsonb, text[]); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.json_object_delete_keys(json jsonb, VARIADIC keys_to_delete text[]) RETURNS jsonb
    LANGUAGE sql IMMUTABLE STRICT
    AS $$
                   SELECT COALESCE ((SELECT ('{' || string_agg(to_json("key") || ':' || "value", ',') || '}')
                                       FROM jsonb_each("json")
                                      WHERE "key" <> ALL ("keys_to_delete")),
                                    '{}')::text::jsonb $$;


ALTER FUNCTION public.json_object_delete_keys(json jsonb, VARIADIC keys_to_delete text[]) OWNER TO taiga;

--
-- Name: reduce_dim(anyarray); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.reduce_dim(anyarray) RETURNS SETOF anyarray
    LANGUAGE plpgsql IMMUTABLE
    AS $_$
            DECLARE
                s $1%TYPE;
            BEGIN
                IF $1 = '{}' THEN
                	RETURN;
                END IF;
                FOREACH s SLICE 1 IN ARRAY $1 LOOP
                    RETURN NEXT s;
                END LOOP;
                RETURN;
            END;
            $_$;


ALTER FUNCTION public.reduce_dim(anyarray) OWNER TO taiga;

--
-- Name: update_project_tags_colors(); Type: FUNCTION; Schema: public; Owner: taiga
--

CREATE FUNCTION public.update_project_tags_colors() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
            DECLARE
            	tags text[];
            	project_tags_colors text[];
            	tag_color text[];
            	project_tags text[];
            	tag text;
            	project_id integer;
            BEGIN
            	tags := NEW.tags::text[];
            	project_id := NEW.project_id::integer;
            	project_tags := '{}';

            	-- Read project tags_colors into project_tags_colors
            	SELECT projects_project.tags_colors INTO project_tags_colors
                FROM projects_project
                WHERE id = project_id;

            	-- Extract just the project tags to project_tags_colors
                IF project_tags_colors != ARRAY[]::text[] THEN
                    FOREACH tag_color SLICE 1 in ARRAY project_tags_colors
                    LOOP
                        project_tags := array_append(project_tags, tag_color[1]);
                    END LOOP;
                END IF;

            	-- Add to project_tags_colors the new tags
                IF tags IS NOT NULL THEN
                    FOREACH tag in ARRAY tags
                    LOOP
                        IF tag != ALL(project_tags) THEN
                            project_tags_colors := array_cat(project_tags_colors,
                                                             ARRAY[ARRAY[tag, NULL]]);
                        END IF;
                    END LOOP;
                END IF;

            	-- Save the result in the tags_colors column
                UPDATE projects_project
                SET tags_colors = project_tags_colors
                WHERE id = project_id;

            	RETURN NULL;
            END; $$;


ALTER FUNCTION public.update_project_tags_colors() OWNER TO taiga;

--
-- Name: array_agg_mult(anyarray); Type: AGGREGATE; Schema: public; Owner: taiga
--

CREATE AGGREGATE public.array_agg_mult(anyarray) (
    SFUNC = array_cat,
    STYPE = anyarray,
    INITCOND = '{}'
);


ALTER AGGREGATE public.array_agg_mult(anyarray) OWNER TO taiga;

--
-- Name: english_stem_nostop; Type: TEXT SEARCH DICTIONARY; Schema: public; Owner: taiga
--

CREATE TEXT SEARCH DICTIONARY public.english_stem_nostop (
    TEMPLATE = pg_catalog.snowball,
    language = 'english' );


ALTER TEXT SEARCH DICTIONARY public.english_stem_nostop OWNER TO taiga;

--
-- Name: english_nostop; Type: TEXT SEARCH CONFIGURATION; Schema: public; Owner: taiga
--

CREATE TEXT SEARCH CONFIGURATION public.english_nostop (
    PARSER = pg_catalog."default" );

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR asciiword WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR word WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR numword WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR email WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR url WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR host WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR sfloat WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR version WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR hword_numpart WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR hword_part WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR hword_asciipart WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR numhword WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR asciihword WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR hword WITH public.english_stem_nostop;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR url_path WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR file WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR "float" WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR "int" WITH simple;

ALTER TEXT SEARCH CONFIGURATION public.english_nostop
    ADD MAPPING FOR uint WITH simple;


ALTER TEXT SEARCH CONFIGURATION public.english_nostop OWNER TO taiga;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: attachments_attachment; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.attachments_attachment (
    id integer NOT NULL,
    object_id integer NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    attached_file character varying(500),
    is_deprecated boolean NOT NULL,
    description text NOT NULL,
    "order" integer NOT NULL,
    content_type_id integer NOT NULL,
    owner_id integer,
    project_id integer NOT NULL,
    name character varying(500) NOT NULL,
    size integer,
    sha1 character varying(40) NOT NULL,
    from_comment boolean NOT NULL,
    CONSTRAINT attachments_attachment_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.attachments_attachment OWNER TO taiga;

--
-- Name: attachments_attachment_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.attachments_attachment_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.attachments_attachment_id_seq OWNER TO taiga;

--
-- Name: attachments_attachment_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.attachments_attachment_id_seq OWNED BY public.attachments_attachment.id;


--
-- Name: auth_group; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.auth_group (
    id integer NOT NULL,
    name character varying(150) NOT NULL
);


ALTER TABLE public.auth_group OWNER TO taiga;

--
-- Name: auth_group_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.auth_group_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.auth_group_id_seq OWNER TO taiga;

--
-- Name: auth_group_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.auth_group_id_seq OWNED BY public.auth_group.id;


--
-- Name: auth_group_permissions; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.auth_group_permissions (
    id integer NOT NULL,
    group_id integer NOT NULL,
    permission_id integer NOT NULL
);


ALTER TABLE public.auth_group_permissions OWNER TO taiga;

--
-- Name: auth_group_permissions_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.auth_group_permissions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.auth_group_permissions_id_seq OWNER TO taiga;

--
-- Name: auth_group_permissions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.auth_group_permissions_id_seq OWNED BY public.auth_group_permissions.id;


--
-- Name: auth_permission; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.auth_permission (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    content_type_id integer NOT NULL,
    codename character varying(100) NOT NULL
);


ALTER TABLE public.auth_permission OWNER TO taiga;

--
-- Name: auth_permission_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.auth_permission_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.auth_permission_id_seq OWNER TO taiga;

--
-- Name: auth_permission_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.auth_permission_id_seq OWNED BY public.auth_permission.id;


--
-- Name: contact_contactentry; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.contact_contactentry (
    id integer NOT NULL,
    comment text NOT NULL,
    created_date timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.contact_contactentry OWNER TO taiga;

--
-- Name: contact_contactentry_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.contact_contactentry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.contact_contactentry_id_seq OWNER TO taiga;

--
-- Name: contact_contactentry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.contact_contactentry_id_seq OWNED BY public.contact_contactentry.id;


--
-- Name: custom_attributes_epiccustomattribute; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_epiccustomattribute (
    id integer NOT NULL,
    name character varying(64) NOT NULL,
    description text NOT NULL,
    type character varying(16) NOT NULL,
    "order" bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    extra jsonb
);


ALTER TABLE public.custom_attributes_epiccustomattribute OWNER TO taiga;

--
-- Name: custom_attributes_epiccustomattribute_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_epiccustomattribute_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_epiccustomattribute_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_epiccustomattribute_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_epiccustomattribute_id_seq OWNED BY public.custom_attributes_epiccustomattribute.id;


--
-- Name: custom_attributes_epiccustomattributesvalues; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_epiccustomattributesvalues (
    id integer NOT NULL,
    version integer NOT NULL,
    attributes_values jsonb NOT NULL,
    epic_id integer NOT NULL
);


ALTER TABLE public.custom_attributes_epiccustomattributesvalues OWNER TO taiga;

--
-- Name: custom_attributes_epiccustomattributesvalues_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_epiccustomattributesvalues_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_epiccustomattributesvalues_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_epiccustomattributesvalues_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_epiccustomattributesvalues_id_seq OWNED BY public.custom_attributes_epiccustomattributesvalues.id;


--
-- Name: custom_attributes_issuecustomattribute; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_issuecustomattribute (
    id integer NOT NULL,
    name character varying(64) NOT NULL,
    description text NOT NULL,
    "order" bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    type character varying(16) NOT NULL,
    extra jsonb
);


ALTER TABLE public.custom_attributes_issuecustomattribute OWNER TO taiga;

--
-- Name: custom_attributes_issuecustomattribute_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_issuecustomattribute_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_issuecustomattribute_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_issuecustomattribute_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_issuecustomattribute_id_seq OWNED BY public.custom_attributes_issuecustomattribute.id;


--
-- Name: custom_attributes_issuecustomattributesvalues; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_issuecustomattributesvalues (
    id integer NOT NULL,
    version integer NOT NULL,
    attributes_values jsonb NOT NULL,
    issue_id integer NOT NULL
);


ALTER TABLE public.custom_attributes_issuecustomattributesvalues OWNER TO taiga;

--
-- Name: custom_attributes_issuecustomattributesvalues_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_issuecustomattributesvalues_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_issuecustomattributesvalues_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_issuecustomattributesvalues_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_issuecustomattributesvalues_id_seq OWNED BY public.custom_attributes_issuecustomattributesvalues.id;


--
-- Name: custom_attributes_taskcustomattribute; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_taskcustomattribute (
    id integer NOT NULL,
    name character varying(64) NOT NULL,
    description text NOT NULL,
    "order" bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    type character varying(16) NOT NULL,
    extra jsonb
);


ALTER TABLE public.custom_attributes_taskcustomattribute OWNER TO taiga;

--
-- Name: custom_attributes_taskcustomattribute_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_taskcustomattribute_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_taskcustomattribute_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_taskcustomattribute_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_taskcustomattribute_id_seq OWNED BY public.custom_attributes_taskcustomattribute.id;


--
-- Name: custom_attributes_taskcustomattributesvalues; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_taskcustomattributesvalues (
    id integer NOT NULL,
    version integer NOT NULL,
    attributes_values jsonb NOT NULL,
    task_id integer NOT NULL
);


ALTER TABLE public.custom_attributes_taskcustomattributesvalues OWNER TO taiga;

--
-- Name: custom_attributes_taskcustomattributesvalues_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_taskcustomattributesvalues_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_taskcustomattributesvalues_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_taskcustomattributesvalues_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_taskcustomattributesvalues_id_seq OWNED BY public.custom_attributes_taskcustomattributesvalues.id;


--
-- Name: custom_attributes_userstorycustomattribute; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_userstorycustomattribute (
    id integer NOT NULL,
    name character varying(64) NOT NULL,
    description text NOT NULL,
    "order" bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    type character varying(16) NOT NULL,
    extra jsonb
);


ALTER TABLE public.custom_attributes_userstorycustomattribute OWNER TO taiga;

--
-- Name: custom_attributes_userstorycustomattribute_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_userstorycustomattribute_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_userstorycustomattribute_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_userstorycustomattribute_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_userstorycustomattribute_id_seq OWNED BY public.custom_attributes_userstorycustomattribute.id;


--
-- Name: custom_attributes_userstorycustomattributesvalues; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.custom_attributes_userstorycustomattributesvalues (
    id integer NOT NULL,
    version integer NOT NULL,
    attributes_values jsonb NOT NULL,
    user_story_id integer NOT NULL
);


ALTER TABLE public.custom_attributes_userstorycustomattributesvalues OWNER TO taiga;

--
-- Name: custom_attributes_userstorycustomattributesvalues_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.custom_attributes_userstorycustomattributesvalues_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.custom_attributes_userstorycustomattributesvalues_id_seq OWNER TO taiga;

--
-- Name: custom_attributes_userstorycustomattributesvalues_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.custom_attributes_userstorycustomattributesvalues_id_seq OWNED BY public.custom_attributes_userstorycustomattributesvalues.id;


--
-- Name: django_admin_log; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.django_admin_log (
    id integer NOT NULL,
    action_time timestamp with time zone NOT NULL,
    object_id text,
    object_repr character varying(200) NOT NULL,
    action_flag smallint NOT NULL,
    change_message text NOT NULL,
    content_type_id integer,
    user_id integer NOT NULL,
    CONSTRAINT django_admin_log_action_flag_check CHECK ((action_flag >= 0))
);


ALTER TABLE public.django_admin_log OWNER TO taiga;

--
-- Name: django_admin_log_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.django_admin_log_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.django_admin_log_id_seq OWNER TO taiga;

--
-- Name: django_admin_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.django_admin_log_id_seq OWNED BY public.django_admin_log.id;


--
-- Name: django_content_type; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.django_content_type (
    id integer NOT NULL,
    app_label character varying(100) NOT NULL,
    model character varying(100) NOT NULL
);


ALTER TABLE public.django_content_type OWNER TO taiga;

--
-- Name: django_content_type_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.django_content_type_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.django_content_type_id_seq OWNER TO taiga;

--
-- Name: django_content_type_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.django_content_type_id_seq OWNED BY public.django_content_type.id;


--
-- Name: django_migrations; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.django_migrations (
    id integer NOT NULL,
    app character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    applied timestamp with time zone NOT NULL
);


ALTER TABLE public.django_migrations OWNER TO taiga;

--
-- Name: django_migrations_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.django_migrations_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.django_migrations_id_seq OWNER TO taiga;

--
-- Name: django_migrations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.django_migrations_id_seq OWNED BY public.django_migrations.id;


--
-- Name: django_session; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.django_session (
    session_key character varying(40) NOT NULL,
    session_data text NOT NULL,
    expire_date timestamp with time zone NOT NULL
);


ALTER TABLE public.django_session OWNER TO taiga;

--
-- Name: djmail_message; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.djmail_message (
    uuid character varying(40) NOT NULL,
    from_email character varying(1024) NOT NULL,
    to_email text NOT NULL,
    body_text text NOT NULL,
    body_html text NOT NULL,
    subject character varying(1024) NOT NULL,
    data text NOT NULL,
    retry_count smallint NOT NULL,
    status smallint NOT NULL,
    priority smallint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    sent_at timestamp with time zone,
    exception text NOT NULL
);


ALTER TABLE public.djmail_message OWNER TO taiga;

--
-- Name: easy_thumbnails_source; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.easy_thumbnails_source (
    id integer NOT NULL,
    storage_hash character varying(40) NOT NULL,
    name character varying(255) NOT NULL,
    modified timestamp with time zone NOT NULL
);


ALTER TABLE public.easy_thumbnails_source OWNER TO taiga;

--
-- Name: easy_thumbnails_source_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.easy_thumbnails_source_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.easy_thumbnails_source_id_seq OWNER TO taiga;

--
-- Name: easy_thumbnails_source_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.easy_thumbnails_source_id_seq OWNED BY public.easy_thumbnails_source.id;


--
-- Name: easy_thumbnails_thumbnail; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.easy_thumbnails_thumbnail (
    id integer NOT NULL,
    storage_hash character varying(40) NOT NULL,
    name character varying(255) NOT NULL,
    modified timestamp with time zone NOT NULL,
    source_id integer NOT NULL
);


ALTER TABLE public.easy_thumbnails_thumbnail OWNER TO taiga;

--
-- Name: easy_thumbnails_thumbnail_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.easy_thumbnails_thumbnail_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.easy_thumbnails_thumbnail_id_seq OWNER TO taiga;

--
-- Name: easy_thumbnails_thumbnail_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.easy_thumbnails_thumbnail_id_seq OWNED BY public.easy_thumbnails_thumbnail.id;


--
-- Name: easy_thumbnails_thumbnaildimensions; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.easy_thumbnails_thumbnaildimensions (
    id integer NOT NULL,
    thumbnail_id integer NOT NULL,
    width integer,
    height integer,
    CONSTRAINT easy_thumbnails_thumbnaildimensions_height_check CHECK ((height >= 0)),
    CONSTRAINT easy_thumbnails_thumbnaildimensions_width_check CHECK ((width >= 0))
);


ALTER TABLE public.easy_thumbnails_thumbnaildimensions OWNER TO taiga;

--
-- Name: easy_thumbnails_thumbnaildimensions_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.easy_thumbnails_thumbnaildimensions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.easy_thumbnails_thumbnaildimensions_id_seq OWNER TO taiga;

--
-- Name: easy_thumbnails_thumbnaildimensions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.easy_thumbnails_thumbnaildimensions_id_seq OWNED BY public.easy_thumbnails_thumbnaildimensions.id;


--
-- Name: epics_epic; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.epics_epic (
    id integer NOT NULL,
    tags text[],
    version integer NOT NULL,
    is_blocked boolean NOT NULL,
    blocked_note text NOT NULL,
    ref bigint,
    epics_order bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    subject text NOT NULL,
    description text NOT NULL,
    client_requirement boolean NOT NULL,
    team_requirement boolean NOT NULL,
    assigned_to_id integer,
    owner_id integer,
    project_id integer NOT NULL,
    status_id integer,
    color character varying(32) NOT NULL,
    external_reference text[]
);


ALTER TABLE public.epics_epic OWNER TO taiga;

--
-- Name: epics_epic_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.epics_epic_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.epics_epic_id_seq OWNER TO taiga;

--
-- Name: epics_epic_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.epics_epic_id_seq OWNED BY public.epics_epic.id;


--
-- Name: epics_relateduserstory; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.epics_relateduserstory (
    id integer NOT NULL,
    "order" bigint NOT NULL,
    epic_id integer NOT NULL,
    user_story_id integer NOT NULL
);


ALTER TABLE public.epics_relateduserstory OWNER TO taiga;

--
-- Name: epics_relateduserstory_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.epics_relateduserstory_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.epics_relateduserstory_id_seq OWNER TO taiga;

--
-- Name: epics_relateduserstory_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.epics_relateduserstory_id_seq OWNED BY public.epics_relateduserstory.id;


--
-- Name: external_apps_application; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.external_apps_application (
    id character varying(255) NOT NULL,
    name character varying(255) NOT NULL,
    icon_url text,
    web character varying(255),
    description text,
    next_url text NOT NULL
);


ALTER TABLE public.external_apps_application OWNER TO taiga;

--
-- Name: external_apps_applicationtoken; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.external_apps_applicationtoken (
    id integer NOT NULL,
    auth_code character varying(255),
    token character varying(255),
    state character varying(255),
    application_id character varying(255) NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.external_apps_applicationtoken OWNER TO taiga;

--
-- Name: external_apps_applicationtoken_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.external_apps_applicationtoken_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.external_apps_applicationtoken_id_seq OWNER TO taiga;

--
-- Name: external_apps_applicationtoken_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.external_apps_applicationtoken_id_seq OWNED BY public.external_apps_applicationtoken.id;


--
-- Name: feedback_feedbackentry; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.feedback_feedbackentry (
    id integer NOT NULL,
    full_name character varying(256) NOT NULL,
    email character varying(255) NOT NULL,
    comment text NOT NULL,
    created_date timestamp with time zone NOT NULL
);


ALTER TABLE public.feedback_feedbackentry OWNER TO taiga;

--
-- Name: feedback_feedbackentry_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.feedback_feedbackentry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.feedback_feedbackentry_id_seq OWNER TO taiga;

--
-- Name: feedback_feedbackentry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.feedback_feedbackentry_id_seq OWNED BY public.feedback_feedbackentry.id;


--
-- Name: history_historyentry; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.history_historyentry (
    id character varying(255) NOT NULL,
    "user" jsonb,
    created_at timestamp with time zone,
    type smallint,
    is_snapshot boolean,
    key character varying(255),
    diff jsonb,
    snapshot jsonb,
    "values" jsonb,
    comment text,
    comment_html text,
    delete_comment_date timestamp with time zone,
    delete_comment_user jsonb,
    is_hidden boolean,
    comment_versions jsonb,
    edit_comment_date timestamp with time zone,
    project_id integer NOT NULL,
    values_diff_cache jsonb
);


ALTER TABLE public.history_historyentry OWNER TO taiga;

--
-- Name: issues_issue; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.issues_issue (
    id integer NOT NULL,
    tags text[],
    version integer NOT NULL,
    is_blocked boolean NOT NULL,
    blocked_note text NOT NULL,
    ref bigint,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    finished_date timestamp with time zone,
    subject text NOT NULL,
    description text NOT NULL,
    assigned_to_id integer,
    milestone_id integer,
    owner_id integer,
    priority_id integer,
    project_id integer NOT NULL,
    severity_id integer,
    status_id integer,
    type_id integer,
    external_reference text[],
    due_date date,
    due_date_reason text NOT NULL
);


ALTER TABLE public.issues_issue OWNER TO taiga;

--
-- Name: issues_issue_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.issues_issue_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.issues_issue_id_seq OWNER TO taiga;

--
-- Name: issues_issue_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.issues_issue_id_seq OWNED BY public.issues_issue.id;


--
-- Name: likes_like; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.likes_like (
    id integer NOT NULL,
    object_id integer NOT NULL,
    created_date timestamp with time zone NOT NULL,
    content_type_id integer NOT NULL,
    user_id integer NOT NULL,
    CONSTRAINT likes_like_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.likes_like OWNER TO taiga;

--
-- Name: likes_like_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.likes_like_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.likes_like_id_seq OWNER TO taiga;

--
-- Name: likes_like_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.likes_like_id_seq OWNED BY public.likes_like.id;


--
-- Name: milestones_milestone; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.milestones_milestone (
    id integer NOT NULL,
    name character varying(200) NOT NULL,
    slug character varying(250) NOT NULL,
    estimated_start date NOT NULL,
    estimated_finish date NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    closed boolean NOT NULL,
    disponibility double precision,
    "order" smallint NOT NULL,
    owner_id integer,
    project_id integer NOT NULL,
    CONSTRAINT milestones_milestone_order_check CHECK (("order" >= 0))
);


ALTER TABLE public.milestones_milestone OWNER TO taiga;

--
-- Name: milestones_milestone_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.milestones_milestone_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.milestones_milestone_id_seq OWNER TO taiga;

--
-- Name: milestones_milestone_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.milestones_milestone_id_seq OWNED BY public.milestones_milestone.id;


--
-- Name: notifications_historychangenotification; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_historychangenotification (
    id integer NOT NULL,
    key character varying(255) NOT NULL,
    created_datetime timestamp with time zone NOT NULL,
    updated_datetime timestamp with time zone NOT NULL,
    history_type smallint NOT NULL,
    owner_id integer NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.notifications_historychangenotification OWNER TO taiga;

--
-- Name: notifications_historychangenotification_history_entries; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_historychangenotification_history_entries (
    id integer NOT NULL,
    historychangenotification_id integer NOT NULL,
    historyentry_id character varying(255) NOT NULL
);


ALTER TABLE public.notifications_historychangenotification_history_entries OWNER TO taiga;

--
-- Name: notifications_historychangenotification_history_entries_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_historychangenotification_history_entries_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_historychangenotification_history_entries_id_seq OWNER TO taiga;

--
-- Name: notifications_historychangenotification_history_entries_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_historychangenotification_history_entries_id_seq OWNED BY public.notifications_historychangenotification_history_entries.id;


--
-- Name: notifications_historychangenotification_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_historychangenotification_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_historychangenotification_id_seq OWNER TO taiga;

--
-- Name: notifications_historychangenotification_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_historychangenotification_id_seq OWNED BY public.notifications_historychangenotification.id;


--
-- Name: notifications_historychangenotification_notify_users; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_historychangenotification_notify_users (
    id integer NOT NULL,
    historychangenotification_id integer NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.notifications_historychangenotification_notify_users OWNER TO taiga;

--
-- Name: notifications_historychangenotification_notify_users_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_historychangenotification_notify_users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_historychangenotification_notify_users_id_seq OWNER TO taiga;

--
-- Name: notifications_historychangenotification_notify_users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_historychangenotification_notify_users_id_seq OWNED BY public.notifications_historychangenotification_notify_users.id;


--
-- Name: notifications_notifypolicy; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_notifypolicy (
    id integer NOT NULL,
    notify_level smallint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    modified_at timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    user_id integer NOT NULL,
    live_notify_level smallint NOT NULL,
    web_notify_level boolean NOT NULL
);


ALTER TABLE public.notifications_notifypolicy OWNER TO taiga;

--
-- Name: notifications_notifypolicy_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_notifypolicy_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_notifypolicy_id_seq OWNER TO taiga;

--
-- Name: notifications_notifypolicy_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_notifypolicy_id_seq OWNED BY public.notifications_notifypolicy.id;


--
-- Name: notifications_watched; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_watched (
    id integer NOT NULL,
    object_id integer NOT NULL,
    created_date timestamp with time zone NOT NULL,
    content_type_id integer NOT NULL,
    user_id integer NOT NULL,
    project_id integer NOT NULL,
    CONSTRAINT notifications_watched_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.notifications_watched OWNER TO taiga;

--
-- Name: notifications_watched_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_watched_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_watched_id_seq OWNER TO taiga;

--
-- Name: notifications_watched_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_watched_id_seq OWNED BY public.notifications_watched.id;


--
-- Name: notifications_webnotification; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.notifications_webnotification (
    id integer NOT NULL,
    created timestamp with time zone NOT NULL,
    read timestamp with time zone,
    event_type integer NOT NULL,
    data jsonb NOT NULL,
    user_id integer NOT NULL,
    CONSTRAINT notifications_webnotification_event_type_check CHECK ((event_type >= 0))
);


ALTER TABLE public.notifications_webnotification OWNER TO taiga;

--
-- Name: notifications_webnotification_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.notifications_webnotification_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.notifications_webnotification_id_seq OWNER TO taiga;

--
-- Name: notifications_webnotification_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.notifications_webnotification_id_seq OWNED BY public.notifications_webnotification.id;


--
-- Name: projects_epicstatus; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_epicstatus (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    slug character varying(255) NOT NULL,
    "order" integer NOT NULL,
    is_closed boolean NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_epicstatus OWNER TO taiga;

--
-- Name: projects_epicstatus_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_epicstatus_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_epicstatus_id_seq OWNER TO taiga;

--
-- Name: projects_epicstatus_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_epicstatus_id_seq OWNED BY public.projects_epicstatus.id;


--
-- Name: projects_issueduedate; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_issueduedate (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    by_default boolean NOT NULL,
    color character varying(20) NOT NULL,
    days_to_due integer,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_issueduedate OWNER TO taiga;

--
-- Name: projects_issueduedate_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_issueduedate_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_issueduedate_id_seq OWNER TO taiga;

--
-- Name: projects_issueduedate_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_issueduedate_id_seq OWNED BY public.projects_issueduedate.id;


--
-- Name: projects_issuestatus; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_issuestatus (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    is_closed boolean NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL,
    slug character varying(255) NOT NULL
);


ALTER TABLE public.projects_issuestatus OWNER TO taiga;

--
-- Name: projects_issuestatus_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_issuestatus_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_issuestatus_id_seq OWNER TO taiga;

--
-- Name: projects_issuestatus_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_issuestatus_id_seq OWNED BY public.projects_issuestatus.id;


--
-- Name: projects_issuetype; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_issuetype (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_issuetype OWNER TO taiga;

--
-- Name: projects_issuetype_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_issuetype_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_issuetype_id_seq OWNER TO taiga;

--
-- Name: projects_issuetype_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_issuetype_id_seq OWNED BY public.projects_issuetype.id;


--
-- Name: projects_membership; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_membership (
    id integer NOT NULL,
    is_admin boolean NOT NULL,
    email character varying(255),
    created_at timestamp with time zone NOT NULL,
    token character varying(60),
    user_id integer,
    project_id integer NOT NULL,
    role_id integer NOT NULL,
    invited_by_id integer,
    invitation_extra_text text,
    user_order bigint NOT NULL
);


ALTER TABLE public.projects_membership OWNER TO taiga;

--
-- Name: projects_membership_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_membership_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_membership_id_seq OWNER TO taiga;

--
-- Name: projects_membership_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_membership_id_seq OWNED BY public.projects_membership.id;


--
-- Name: projects_points; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_points (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    value double precision,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_points OWNER TO taiga;

--
-- Name: projects_points_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_points_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_points_id_seq OWNER TO taiga;

--
-- Name: projects_points_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_points_id_seq OWNED BY public.projects_points.id;


--
-- Name: projects_priority; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_priority (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_priority OWNER TO taiga;

--
-- Name: projects_priority_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_priority_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_priority_id_seq OWNER TO taiga;

--
-- Name: projects_priority_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_priority_id_seq OWNED BY public.projects_priority.id;


--
-- Name: projects_project; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_project (
    id integer NOT NULL,
    tags text[],
    name character varying(250) NOT NULL,
    slug character varying(250) NOT NULL,
    description text NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    total_milestones integer,
    total_story_points double precision,
    is_backlog_activated boolean NOT NULL,
    is_kanban_activated boolean NOT NULL,
    is_wiki_activated boolean NOT NULL,
    is_issues_activated boolean NOT NULL,
    videoconferences character varying(250),
    videoconferences_extra_data character varying(250),
    anon_permissions text[],
    public_permissions text[],
    is_private boolean NOT NULL,
    tags_colors text[],
    owner_id integer,
    creation_template_id integer,
    default_issue_status_id integer,
    default_issue_type_id integer,
    default_points_id integer,
    default_priority_id integer,
    default_severity_id integer,
    default_task_status_id integer,
    default_us_status_id integer,
    issues_csv_uuid character varying(32),
    tasks_csv_uuid character varying(32),
    userstories_csv_uuid character varying(32),
    is_featured boolean NOT NULL,
    is_looking_for_people boolean NOT NULL,
    total_activity integer NOT NULL,
    total_activity_last_month integer NOT NULL,
    total_activity_last_week integer NOT NULL,
    total_activity_last_year integer NOT NULL,
    total_fans integer NOT NULL,
    total_fans_last_month integer NOT NULL,
    total_fans_last_week integer NOT NULL,
    total_fans_last_year integer NOT NULL,
    totals_updated_datetime timestamp with time zone NOT NULL,
    logo character varying(500),
    looking_for_people_note text NOT NULL,
    blocked_code character varying(255),
    transfer_token character varying(255),
    is_epics_activated boolean NOT NULL,
    default_epic_status_id integer,
    epics_csv_uuid character varying(32),
    is_contact_activated boolean NOT NULL,
    default_swimlane_id integer,
    CONSTRAINT projects_project_total_activity_check CHECK ((total_activity >= 0)),
    CONSTRAINT projects_project_total_activity_last_month_check CHECK ((total_activity_last_month >= 0)),
    CONSTRAINT projects_project_total_activity_last_week_check CHECK ((total_activity_last_week >= 0)),
    CONSTRAINT projects_project_total_activity_last_year_check CHECK ((total_activity_last_year >= 0)),
    CONSTRAINT projects_project_total_fans_check CHECK ((total_fans >= 0)),
    CONSTRAINT projects_project_total_fans_last_month_check CHECK ((total_fans_last_month >= 0)),
    CONSTRAINT projects_project_total_fans_last_week_check CHECK ((total_fans_last_week >= 0)),
    CONSTRAINT projects_project_total_fans_last_year_check CHECK ((total_fans_last_year >= 0))
);


ALTER TABLE public.projects_project OWNER TO taiga;

--
-- Name: projects_project_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_project_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_project_id_seq OWNER TO taiga;

--
-- Name: projects_project_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_project_id_seq OWNED BY public.projects_project.id;


--
-- Name: projects_projectmodulesconfig; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_projectmodulesconfig (
    id integer NOT NULL,
    config jsonb,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_projectmodulesconfig OWNER TO taiga;

--
-- Name: projects_projectmodulesconfig_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_projectmodulesconfig_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_projectmodulesconfig_id_seq OWNER TO taiga;

--
-- Name: projects_projectmodulesconfig_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_projectmodulesconfig_id_seq OWNED BY public.projects_projectmodulesconfig.id;


--
-- Name: projects_projecttemplate; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_projecttemplate (
    id integer NOT NULL,
    name character varying(250) NOT NULL,
    slug character varying(250) NOT NULL,
    description text NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    default_owner_role character varying(50) NOT NULL,
    is_backlog_activated boolean NOT NULL,
    is_kanban_activated boolean NOT NULL,
    is_wiki_activated boolean NOT NULL,
    is_issues_activated boolean NOT NULL,
    videoconferences character varying(250),
    videoconferences_extra_data character varying(250),
    default_options jsonb,
    us_statuses jsonb,
    points jsonb,
    task_statuses jsonb,
    issue_statuses jsonb,
    issue_types jsonb,
    priorities jsonb,
    severities jsonb,
    roles jsonb,
    "order" bigint NOT NULL,
    epic_statuses jsonb,
    is_epics_activated boolean NOT NULL,
    is_contact_activated boolean NOT NULL,
    epic_custom_attributes jsonb,
    is_looking_for_people boolean NOT NULL,
    issue_custom_attributes jsonb,
    looking_for_people_note text NOT NULL,
    tags text[],
    tags_colors text[],
    task_custom_attributes jsonb,
    us_custom_attributes jsonb,
    issue_duedates jsonb,
    task_duedates jsonb,
    us_duedates jsonb
);


ALTER TABLE public.projects_projecttemplate OWNER TO taiga;

--
-- Name: projects_projecttemplate_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_projecttemplate_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_projecttemplate_id_seq OWNER TO taiga;

--
-- Name: projects_projecttemplate_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_projecttemplate_id_seq OWNED BY public.projects_projecttemplate.id;


--
-- Name: projects_severity; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_severity (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_severity OWNER TO taiga;

--
-- Name: projects_severity_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_severity_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_severity_id_seq OWNER TO taiga;

--
-- Name: projects_severity_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_severity_id_seq OWNED BY public.projects_severity.id;


--
-- Name: projects_swimlane; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_swimlane (
    id integer NOT NULL,
    name text NOT NULL,
    "order" bigint NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_swimlane OWNER TO taiga;

--
-- Name: projects_swimlane_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_swimlane_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_swimlane_id_seq OWNER TO taiga;

--
-- Name: projects_swimlane_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_swimlane_id_seq OWNED BY public.projects_swimlane.id;


--
-- Name: projects_swimlaneuserstorystatus; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_swimlaneuserstorystatus (
    id integer NOT NULL,
    wip_limit integer,
    status_id integer NOT NULL,
    swimlane_id integer NOT NULL
);


ALTER TABLE public.projects_swimlaneuserstorystatus OWNER TO taiga;

--
-- Name: projects_swimlaneuserstorystatus_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_swimlaneuserstorystatus_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_swimlaneuserstorystatus_id_seq OWNER TO taiga;

--
-- Name: projects_swimlaneuserstorystatus_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_swimlaneuserstorystatus_id_seq OWNED BY public.projects_swimlaneuserstorystatus.id;


--
-- Name: projects_taskduedate; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_taskduedate (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    by_default boolean NOT NULL,
    color character varying(20) NOT NULL,
    days_to_due integer,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_taskduedate OWNER TO taiga;

--
-- Name: projects_taskduedate_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_taskduedate_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_taskduedate_id_seq OWNER TO taiga;

--
-- Name: projects_taskduedate_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_taskduedate_id_seq OWNED BY public.projects_taskduedate.id;


--
-- Name: projects_taskstatus; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_taskstatus (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    is_closed boolean NOT NULL,
    color character varying(20) NOT NULL,
    project_id integer NOT NULL,
    slug character varying(255) NOT NULL
);


ALTER TABLE public.projects_taskstatus OWNER TO taiga;

--
-- Name: projects_taskstatus_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_taskstatus_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_taskstatus_id_seq OWNER TO taiga;

--
-- Name: projects_taskstatus_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_taskstatus_id_seq OWNED BY public.projects_taskstatus.id;


--
-- Name: projects_userstoryduedate; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_userstoryduedate (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    by_default boolean NOT NULL,
    color character varying(20) NOT NULL,
    days_to_due integer,
    project_id integer NOT NULL
);


ALTER TABLE public.projects_userstoryduedate OWNER TO taiga;

--
-- Name: projects_userstoryduedate_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_userstoryduedate_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_userstoryduedate_id_seq OWNER TO taiga;

--
-- Name: projects_userstoryduedate_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_userstoryduedate_id_seq OWNED BY public.projects_userstoryduedate.id;


--
-- Name: projects_userstorystatus; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.projects_userstorystatus (
    id integer NOT NULL,
    name character varying(255) NOT NULL,
    "order" integer NOT NULL,
    is_closed boolean NOT NULL,
    color character varying(20) NOT NULL,
    wip_limit integer,
    project_id integer NOT NULL,
    slug character varying(255) NOT NULL,
    is_archived boolean NOT NULL
);


ALTER TABLE public.projects_userstorystatus OWNER TO taiga;

--
-- Name: projects_userstorystatus_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.projects_userstorystatus_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.projects_userstorystatus_id_seq OWNER TO taiga;

--
-- Name: projects_userstorystatus_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.projects_userstorystatus_id_seq OWNED BY public.projects_userstorystatus.id;


--
-- Name: references_reference; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.references_reference (
    id integer NOT NULL,
    object_id integer NOT NULL,
    ref bigint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    content_type_id integer NOT NULL,
    project_id integer NOT NULL,
    CONSTRAINT references_reference_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.references_reference OWNER TO taiga;

--
-- Name: references_reference_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.references_reference_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.references_reference_id_seq OWNER TO taiga;

--
-- Name: references_reference_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.references_reference_id_seq OWNED BY public.references_reference.id;


--
-- Name: settings_userprojectsettings; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.settings_userprojectsettings (
    id integer NOT NULL,
    homepage smallint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    modified_at timestamp with time zone NOT NULL,
    project_id integer NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.settings_userprojectsettings OWNER TO taiga;

--
-- Name: settings_userprojectsettings_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.settings_userprojectsettings_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.settings_userprojectsettings_id_seq OWNER TO taiga;

--
-- Name: settings_userprojectsettings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.settings_userprojectsettings_id_seq OWNED BY public.settings_userprojectsettings.id;


--
-- Name: tasks_task; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.tasks_task (
    id integer NOT NULL,
    tags text[],
    version integer NOT NULL,
    is_blocked boolean NOT NULL,
    blocked_note text NOT NULL,
    ref bigint,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    finished_date timestamp with time zone,
    subject text NOT NULL,
    description text NOT NULL,
    is_iocaine boolean NOT NULL,
    assigned_to_id integer,
    milestone_id integer,
    owner_id integer,
    project_id integer NOT NULL,
    status_id integer,
    user_story_id integer,
    taskboard_order bigint NOT NULL,
    us_order bigint NOT NULL,
    external_reference text[],
    due_date date,
    due_date_reason text NOT NULL
);


ALTER TABLE public.tasks_task OWNER TO taiga;

--
-- Name: tasks_task_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.tasks_task_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.tasks_task_id_seq OWNER TO taiga;

--
-- Name: tasks_task_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.tasks_task_id_seq OWNED BY public.tasks_task.id;


--
-- Name: telemetry_instancetelemetry; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.telemetry_instancetelemetry (
    id integer NOT NULL,
    instance_id character varying(100) NOT NULL,
    created_at timestamp with time zone NOT NULL
);


ALTER TABLE public.telemetry_instancetelemetry OWNER TO taiga;

--
-- Name: telemetry_instancetelemetry_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.telemetry_instancetelemetry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.telemetry_instancetelemetry_id_seq OWNER TO taiga;

--
-- Name: telemetry_instancetelemetry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.telemetry_instancetelemetry_id_seq OWNED BY public.telemetry_instancetelemetry.id;


--
-- Name: timeline_timeline; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.timeline_timeline (
    id integer NOT NULL,
    object_id integer NOT NULL,
    namespace character varying(250) NOT NULL,
    event_type character varying(250) NOT NULL,
    project_id integer,
    data jsonb NOT NULL,
    data_content_type_id integer NOT NULL,
    created timestamp with time zone NOT NULL,
    content_type_id integer NOT NULL,
    CONSTRAINT timeline_timeline_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.timeline_timeline OWNER TO taiga;

--
-- Name: timeline_timeline_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.timeline_timeline_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.timeline_timeline_id_seq OWNER TO taiga;

--
-- Name: timeline_timeline_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.timeline_timeline_id_seq OWNED BY public.timeline_timeline.id;


--
-- Name: token_denylist_denylistedtoken; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.token_denylist_denylistedtoken (
    id bigint NOT NULL,
    denylisted_at timestamp with time zone NOT NULL,
    token_id bigint NOT NULL
);


ALTER TABLE public.token_denylist_denylistedtoken OWNER TO taiga;

--
-- Name: token_denylist_denylistedtoken_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.token_denylist_denylistedtoken_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.token_denylist_denylistedtoken_id_seq OWNER TO taiga;

--
-- Name: token_denylist_denylistedtoken_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.token_denylist_denylistedtoken_id_seq OWNED BY public.token_denylist_denylistedtoken.id;


--
-- Name: token_denylist_outstandingtoken; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.token_denylist_outstandingtoken (
    id bigint NOT NULL,
    jti character varying(255) NOT NULL,
    token text NOT NULL,
    created_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    user_id integer
);


ALTER TABLE public.token_denylist_outstandingtoken OWNER TO taiga;

--
-- Name: token_denylist_outstandingtoken_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.token_denylist_outstandingtoken_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.token_denylist_outstandingtoken_id_seq OWNER TO taiga;

--
-- Name: token_denylist_outstandingtoken_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.token_denylist_outstandingtoken_id_seq OWNED BY public.token_denylist_outstandingtoken.id;


--
-- Name: users_authdata; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.users_authdata (
    id integer NOT NULL,
    key character varying(50) NOT NULL,
    value character varying(300) NOT NULL,
    extra jsonb NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.users_authdata OWNER TO taiga;

--
-- Name: users_authdata_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.users_authdata_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.users_authdata_id_seq OWNER TO taiga;

--
-- Name: users_authdata_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.users_authdata_id_seq OWNED BY public.users_authdata.id;


--
-- Name: users_role; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.users_role (
    id integer NOT NULL,
    name character varying(200) NOT NULL,
    slug character varying(250) NOT NULL,
    permissions text[],
    "order" integer NOT NULL,
    computable boolean NOT NULL,
    project_id integer
);


ALTER TABLE public.users_role OWNER TO taiga;

--
-- Name: users_role_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.users_role_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.users_role_id_seq OWNER TO taiga;

--
-- Name: users_role_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.users_role_id_seq OWNED BY public.users_role.id;


--
-- Name: users_user; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.users_user (
    id integer NOT NULL,
    password character varying(128) NOT NULL,
    last_login timestamp with time zone,
    is_superuser boolean NOT NULL,
    username character varying(255) NOT NULL,
    email character varying(255) NOT NULL,
    is_active boolean NOT NULL,
    full_name character varying(256) NOT NULL,
    color character varying(9) NOT NULL,
    bio text NOT NULL,
    photo character varying(500),
    date_joined timestamp with time zone NOT NULL,
    lang character varying(20),
    timezone character varying(20),
    colorize_tags boolean NOT NULL,
    token character varying(200),
    email_token character varying(200),
    new_email character varying(254),
    is_system boolean NOT NULL,
    theme character varying(100),
    max_private_projects integer,
    max_public_projects integer,
    max_memberships_private_projects integer,
    max_memberships_public_projects integer,
    uuid character varying(32) NOT NULL,
    accepted_terms boolean NOT NULL,
    read_new_terms boolean NOT NULL,
    verified_email boolean NOT NULL,
    is_staff boolean NOT NULL,
    date_cancelled timestamp with time zone
);


ALTER TABLE public.users_user OWNER TO taiga;

--
-- Name: users_user_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.users_user_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.users_user_id_seq OWNER TO taiga;

--
-- Name: users_user_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.users_user_id_seq OWNED BY public.users_user.id;


--
-- Name: userstorage_storageentry; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.userstorage_storageentry (
    id integer NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    key character varying(255) NOT NULL,
    value jsonb,
    owner_id integer NOT NULL
);


ALTER TABLE public.userstorage_storageentry OWNER TO taiga;

--
-- Name: userstorage_storageentry_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.userstorage_storageentry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.userstorage_storageentry_id_seq OWNER TO taiga;

--
-- Name: userstorage_storageentry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.userstorage_storageentry_id_seq OWNED BY public.userstorage_storageentry.id;


--
-- Name: userstories_rolepoints; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.userstories_rolepoints (
    id integer NOT NULL,
    points_id integer,
    role_id integer NOT NULL,
    user_story_id integer NOT NULL
);


ALTER TABLE public.userstories_rolepoints OWNER TO taiga;

--
-- Name: userstories_rolepoints_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.userstories_rolepoints_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.userstories_rolepoints_id_seq OWNER TO taiga;

--
-- Name: userstories_rolepoints_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.userstories_rolepoints_id_seq OWNED BY public.userstories_rolepoints.id;


--
-- Name: userstories_userstory; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.userstories_userstory (
    id integer NOT NULL,
    tags text[],
    version integer NOT NULL,
    is_blocked boolean NOT NULL,
    blocked_note text NOT NULL,
    ref bigint,
    is_closed boolean NOT NULL,
    backlog_order bigint NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    finish_date timestamp with time zone,
    subject text NOT NULL,
    description text NOT NULL,
    client_requirement boolean NOT NULL,
    team_requirement boolean NOT NULL,
    assigned_to_id integer,
    generated_from_issue_id integer,
    milestone_id integer,
    owner_id integer,
    project_id integer NOT NULL,
    status_id integer,
    sprint_order bigint NOT NULL,
    kanban_order bigint NOT NULL,
    external_reference text[],
    tribe_gig text,
    due_date date,
    due_date_reason text NOT NULL,
    generated_from_task_id integer,
    from_task_ref text,
    swimlane_id integer
);


ALTER TABLE public.userstories_userstory OWNER TO taiga;

--
-- Name: userstories_userstory_assigned_users; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.userstories_userstory_assigned_users (
    id integer NOT NULL,
    userstory_id integer NOT NULL,
    user_id integer NOT NULL
);


ALTER TABLE public.userstories_userstory_assigned_users OWNER TO taiga;

--
-- Name: userstories_userstory_assigned_users_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.userstories_userstory_assigned_users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.userstories_userstory_assigned_users_id_seq OWNER TO taiga;

--
-- Name: userstories_userstory_assigned_users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.userstories_userstory_assigned_users_id_seq OWNED BY public.userstories_userstory_assigned_users.id;


--
-- Name: userstories_userstory_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.userstories_userstory_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.userstories_userstory_id_seq OWNER TO taiga;

--
-- Name: userstories_userstory_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.userstories_userstory_id_seq OWNED BY public.userstories_userstory.id;


--
-- Name: votes_vote; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.votes_vote (
    id integer NOT NULL,
    object_id integer NOT NULL,
    content_type_id integer NOT NULL,
    user_id integer NOT NULL,
    created_date timestamp with time zone NOT NULL,
    CONSTRAINT votes_vote_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.votes_vote OWNER TO taiga;

--
-- Name: votes_vote_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.votes_vote_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.votes_vote_id_seq OWNER TO taiga;

--
-- Name: votes_vote_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.votes_vote_id_seq OWNED BY public.votes_vote.id;


--
-- Name: votes_votes; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.votes_votes (
    id integer NOT NULL,
    object_id integer NOT NULL,
    count integer NOT NULL,
    content_type_id integer NOT NULL,
    CONSTRAINT votes_votes_count_check CHECK ((count >= 0)),
    CONSTRAINT votes_votes_object_id_check CHECK ((object_id >= 0))
);


ALTER TABLE public.votes_votes OWNER TO taiga;

--
-- Name: votes_votes_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.votes_votes_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.votes_votes_id_seq OWNER TO taiga;

--
-- Name: votes_votes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.votes_votes_id_seq OWNED BY public.votes_votes.id;


--
-- Name: webhooks_webhook; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.webhooks_webhook (
    id integer NOT NULL,
    url character varying(200) NOT NULL,
    key text NOT NULL,
    project_id integer NOT NULL,
    name character varying(250) NOT NULL
);


ALTER TABLE public.webhooks_webhook OWNER TO taiga;

--
-- Name: webhooks_webhook_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.webhooks_webhook_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.webhooks_webhook_id_seq OWNER TO taiga;

--
-- Name: webhooks_webhook_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.webhooks_webhook_id_seq OWNED BY public.webhooks_webhook.id;


--
-- Name: webhooks_webhooklog; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.webhooks_webhooklog (
    id integer NOT NULL,
    url character varying(200) NOT NULL,
    status integer NOT NULL,
    request_data jsonb NOT NULL,
    response_data text NOT NULL,
    webhook_id integer NOT NULL,
    created timestamp with time zone NOT NULL,
    duration double precision NOT NULL,
    request_headers jsonb NOT NULL,
    response_headers jsonb NOT NULL
);


ALTER TABLE public.webhooks_webhooklog OWNER TO taiga;

--
-- Name: webhooks_webhooklog_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.webhooks_webhooklog_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.webhooks_webhooklog_id_seq OWNER TO taiga;

--
-- Name: webhooks_webhooklog_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.webhooks_webhooklog_id_seq OWNED BY public.webhooks_webhooklog.id;


--
-- Name: wiki_wikilink; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.wiki_wikilink (
    id integer NOT NULL,
    title character varying(500) NOT NULL,
    href character varying(500) NOT NULL,
    "order" bigint NOT NULL,
    project_id integer NOT NULL
);


ALTER TABLE public.wiki_wikilink OWNER TO taiga;

--
-- Name: wiki_wikilink_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.wiki_wikilink_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.wiki_wikilink_id_seq OWNER TO taiga;

--
-- Name: wiki_wikilink_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.wiki_wikilink_id_seq OWNED BY public.wiki_wikilink.id;


--
-- Name: wiki_wikipage; Type: TABLE; Schema: public; Owner: taiga
--

CREATE TABLE public.wiki_wikipage (
    id integer NOT NULL,
    version integer NOT NULL,
    slug character varying(500) NOT NULL,
    content text NOT NULL,
    created_date timestamp with time zone NOT NULL,
    modified_date timestamp with time zone NOT NULL,
    last_modifier_id integer,
    owner_id integer,
    project_id integer NOT NULL
);


ALTER TABLE public.wiki_wikipage OWNER TO taiga;

--
-- Name: wiki_wikipage_id_seq; Type: SEQUENCE; Schema: public; Owner: taiga
--

CREATE SEQUENCE public.wiki_wikipage_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


ALTER TABLE public.wiki_wikipage_id_seq OWNER TO taiga;

--
-- Name: wiki_wikipage_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: taiga
--

ALTER SEQUENCE public.wiki_wikipage_id_seq OWNED BY public.wiki_wikipage.id;


--
-- Name: attachments_attachment id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.attachments_attachment ALTER COLUMN id SET DEFAULT nextval('public.attachments_attachment_id_seq'::regclass);


--
-- Name: auth_group id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group ALTER COLUMN id SET DEFAULT nextval('public.auth_group_id_seq'::regclass);


--
-- Name: auth_group_permissions id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group_permissions ALTER COLUMN id SET DEFAULT nextval('public.auth_group_permissions_id_seq'::regclass);


--
-- Name: auth_permission id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_permission ALTER COLUMN id SET DEFAULT nextval('public.auth_permission_id_seq'::regclass);


--
-- Name: contact_contactentry id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.contact_contactentry ALTER COLUMN id SET DEFAULT nextval('public.contact_contactentry_id_seq'::regclass);


--
-- Name: custom_attributes_epiccustomattribute id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattribute ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_epiccustomattribute_id_seq'::regclass);


--
-- Name: custom_attributes_epiccustomattributesvalues id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattributesvalues ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_epiccustomattributesvalues_id_seq'::regclass);


--
-- Name: custom_attributes_issuecustomattribute id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattribute ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_issuecustomattribute_id_seq'::regclass);


--
-- Name: custom_attributes_issuecustomattributesvalues id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattributesvalues ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_issuecustomattributesvalues_id_seq'::regclass);


--
-- Name: custom_attributes_taskcustomattribute id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattribute ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_taskcustomattribute_id_seq'::regclass);


--
-- Name: custom_attributes_taskcustomattributesvalues id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattributesvalues ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_taskcustomattributesvalues_id_seq'::regclass);


--
-- Name: custom_attributes_userstorycustomattribute id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattribute ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_userstorycustomattribute_id_seq'::regclass);


--
-- Name: custom_attributes_userstorycustomattributesvalues id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattributesvalues ALTER COLUMN id SET DEFAULT nextval('public.custom_attributes_userstorycustomattributesvalues_id_seq'::regclass);


--
-- Name: django_admin_log id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_admin_log ALTER COLUMN id SET DEFAULT nextval('public.django_admin_log_id_seq'::regclass);


--
-- Name: django_content_type id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_content_type ALTER COLUMN id SET DEFAULT nextval('public.django_content_type_id_seq'::regclass);


--
-- Name: django_migrations id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_migrations ALTER COLUMN id SET DEFAULT nextval('public.django_migrations_id_seq'::regclass);


--
-- Name: easy_thumbnails_source id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_source ALTER COLUMN id SET DEFAULT nextval('public.easy_thumbnails_source_id_seq'::regclass);


--
-- Name: easy_thumbnails_thumbnail id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnail ALTER COLUMN id SET DEFAULT nextval('public.easy_thumbnails_thumbnail_id_seq'::regclass);


--
-- Name: easy_thumbnails_thumbnaildimensions id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnaildimensions ALTER COLUMN id SET DEFAULT nextval('public.easy_thumbnails_thumbnaildimensions_id_seq'::regclass);


--
-- Name: epics_epic id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic ALTER COLUMN id SET DEFAULT nextval('public.epics_epic_id_seq'::regclass);


--
-- Name: epics_relateduserstory id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_relateduserstory ALTER COLUMN id SET DEFAULT nextval('public.epics_relateduserstory_id_seq'::regclass);


--
-- Name: external_apps_applicationtoken id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_applicationtoken ALTER COLUMN id SET DEFAULT nextval('public.external_apps_applicationtoken_id_seq'::regclass);


--
-- Name: feedback_feedbackentry id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.feedback_feedbackentry ALTER COLUMN id SET DEFAULT nextval('public.feedback_feedbackentry_id_seq'::regclass);


--
-- Name: issues_issue id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue ALTER COLUMN id SET DEFAULT nextval('public.issues_issue_id_seq'::regclass);


--
-- Name: likes_like id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.likes_like ALTER COLUMN id SET DEFAULT nextval('public.likes_like_id_seq'::regclass);


--
-- Name: milestones_milestone id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone ALTER COLUMN id SET DEFAULT nextval('public.milestones_milestone_id_seq'::regclass);


--
-- Name: notifications_historychangenotification id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification ALTER COLUMN id SET DEFAULT nextval('public.notifications_historychangenotification_id_seq'::regclass);


--
-- Name: notifications_historychangenotification_history_entries id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_history_entries ALTER COLUMN id SET DEFAULT nextval('public.notifications_historychangenotification_history_entries_id_seq'::regclass);


--
-- Name: notifications_historychangenotification_notify_users id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_notify_users ALTER COLUMN id SET DEFAULT nextval('public.notifications_historychangenotification_notify_users_id_seq'::regclass);


--
-- Name: notifications_notifypolicy id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_notifypolicy ALTER COLUMN id SET DEFAULT nextval('public.notifications_notifypolicy_id_seq'::regclass);


--
-- Name: notifications_watched id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched ALTER COLUMN id SET DEFAULT nextval('public.notifications_watched_id_seq'::regclass);


--
-- Name: notifications_webnotification id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_webnotification ALTER COLUMN id SET DEFAULT nextval('public.notifications_webnotification_id_seq'::regclass);


--
-- Name: projects_epicstatus id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_epicstatus ALTER COLUMN id SET DEFAULT nextval('public.projects_epicstatus_id_seq'::regclass);


--
-- Name: projects_issueduedate id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issueduedate ALTER COLUMN id SET DEFAULT nextval('public.projects_issueduedate_id_seq'::regclass);


--
-- Name: projects_issuestatus id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuestatus ALTER COLUMN id SET DEFAULT nextval('public.projects_issuestatus_id_seq'::regclass);


--
-- Name: projects_issuetype id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuetype ALTER COLUMN id SET DEFAULT nextval('public.projects_issuetype_id_seq'::regclass);


--
-- Name: projects_membership id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership ALTER COLUMN id SET DEFAULT nextval('public.projects_membership_id_seq'::regclass);


--
-- Name: projects_points id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_points ALTER COLUMN id SET DEFAULT nextval('public.projects_points_id_seq'::regclass);


--
-- Name: projects_priority id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_priority ALTER COLUMN id SET DEFAULT nextval('public.projects_priority_id_seq'::regclass);


--
-- Name: projects_project id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project ALTER COLUMN id SET DEFAULT nextval('public.projects_project_id_seq'::regclass);


--
-- Name: projects_projectmodulesconfig id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projectmodulesconfig ALTER COLUMN id SET DEFAULT nextval('public.projects_projectmodulesconfig_id_seq'::regclass);


--
-- Name: projects_projecttemplate id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projecttemplate ALTER COLUMN id SET DEFAULT nextval('public.projects_projecttemplate_id_seq'::regclass);


--
-- Name: projects_severity id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_severity ALTER COLUMN id SET DEFAULT nextval('public.projects_severity_id_seq'::regclass);


--
-- Name: projects_swimlane id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlane ALTER COLUMN id SET DEFAULT nextval('public.projects_swimlane_id_seq'::regclass);


--
-- Name: projects_swimlaneuserstorystatus id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlaneuserstorystatus ALTER COLUMN id SET DEFAULT nextval('public.projects_swimlaneuserstorystatus_id_seq'::regclass);


--
-- Name: projects_taskduedate id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskduedate ALTER COLUMN id SET DEFAULT nextval('public.projects_taskduedate_id_seq'::regclass);


--
-- Name: projects_taskstatus id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskstatus ALTER COLUMN id SET DEFAULT nextval('public.projects_taskstatus_id_seq'::regclass);


--
-- Name: projects_userstoryduedate id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstoryduedate ALTER COLUMN id SET DEFAULT nextval('public.projects_userstoryduedate_id_seq'::regclass);


--
-- Name: projects_userstorystatus id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstorystatus ALTER COLUMN id SET DEFAULT nextval('public.projects_userstorystatus_id_seq'::regclass);


--
-- Name: references_reference id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.references_reference ALTER COLUMN id SET DEFAULT nextval('public.references_reference_id_seq'::regclass);


--
-- Name: settings_userprojectsettings id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.settings_userprojectsettings ALTER COLUMN id SET DEFAULT nextval('public.settings_userprojectsettings_id_seq'::regclass);


--
-- Name: tasks_task id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task ALTER COLUMN id SET DEFAULT nextval('public.tasks_task_id_seq'::regclass);


--
-- Name: telemetry_instancetelemetry id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.telemetry_instancetelemetry ALTER COLUMN id SET DEFAULT nextval('public.telemetry_instancetelemetry_id_seq'::regclass);


--
-- Name: timeline_timeline id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.timeline_timeline ALTER COLUMN id SET DEFAULT nextval('public.timeline_timeline_id_seq'::regclass);


--
-- Name: token_denylist_denylistedtoken id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_denylistedtoken ALTER COLUMN id SET DEFAULT nextval('public.token_denylist_denylistedtoken_id_seq'::regclass);


--
-- Name: token_denylist_outstandingtoken id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_outstandingtoken ALTER COLUMN id SET DEFAULT nextval('public.token_denylist_outstandingtoken_id_seq'::regclass);


--
-- Name: users_authdata id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_authdata ALTER COLUMN id SET DEFAULT nextval('public.users_authdata_id_seq'::regclass);


--
-- Name: users_role id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_role ALTER COLUMN id SET DEFAULT nextval('public.users_role_id_seq'::regclass);


--
-- Name: users_user id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_user ALTER COLUMN id SET DEFAULT nextval('public.users_user_id_seq'::regclass);


--
-- Name: userstorage_storageentry id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstorage_storageentry ALTER COLUMN id SET DEFAULT nextval('public.userstorage_storageentry_id_seq'::regclass);


--
-- Name: userstories_rolepoints id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints ALTER COLUMN id SET DEFAULT nextval('public.userstories_rolepoints_id_seq'::regclass);


--
-- Name: userstories_userstory id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory ALTER COLUMN id SET DEFAULT nextval('public.userstories_userstory_id_seq'::regclass);


--
-- Name: userstories_userstory_assigned_users id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory_assigned_users ALTER COLUMN id SET DEFAULT nextval('public.userstories_userstory_assigned_users_id_seq'::regclass);


--
-- Name: votes_vote id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_vote ALTER COLUMN id SET DEFAULT nextval('public.votes_vote_id_seq'::regclass);


--
-- Name: votes_votes id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_votes ALTER COLUMN id SET DEFAULT nextval('public.votes_votes_id_seq'::regclass);


--
-- Name: webhooks_webhook id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhook ALTER COLUMN id SET DEFAULT nextval('public.webhooks_webhook_id_seq'::regclass);


--
-- Name: webhooks_webhooklog id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhooklog ALTER COLUMN id SET DEFAULT nextval('public.webhooks_webhooklog_id_seq'::regclass);


--
-- Name: wiki_wikilink id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikilink ALTER COLUMN id SET DEFAULT nextval('public.wiki_wikilink_id_seq'::regclass);


--
-- Name: wiki_wikipage id; Type: DEFAULT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage ALTER COLUMN id SET DEFAULT nextval('public.wiki_wikipage_id_seq'::regclass);


--
-- Data for Name: attachments_attachment; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.attachments_attachment (id, object_id, created_date, modified_date, attached_file, is_deprecated, description, "order", content_type_id, owner_id, project_id, name, size, sha1, from_comment) FROM stdin;
\.


--
-- Data for Name: auth_group; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.auth_group (id, name) FROM stdin;
\.


--
-- Data for Name: auth_group_permissions; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.auth_group_permissions (id, group_id, permission_id) FROM stdin;
\.


--
-- Data for Name: auth_permission; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.auth_permission (id, name, content_type_id, codename) FROM stdin;
1	Can add permission	1	add_permission
2	Can change permission	1	change_permission
3	Can delete permission	1	delete_permission
4	Can view permission	1	view_permission
5	Can add group	2	add_group
6	Can change group	2	change_group
7	Can delete group	2	delete_group
8	Can view group	2	view_group
9	Can add content type	3	add_contenttype
10	Can change content type	3	change_contenttype
11	Can delete content type	3	delete_contenttype
12	Can view content type	3	view_contenttype
13	Can add session	4	add_session
14	Can change session	4	change_session
15	Can delete session	4	delete_session
16	Can view session	4	view_session
17	Can add log entry	5	add_logentry
18	Can change log entry	5	change_logentry
19	Can delete log entry	5	delete_logentry
20	Can view log entry	5	view_logentry
21	Can add user	6	add_user
22	Can change user	6	change_user
23	Can delete user	6	delete_user
24	Can view user	6	view_user
25	Can add role	7	add_role
26	Can change role	7	change_role
27	Can delete role	7	delete_role
28	Can view role	7	view_role
29	Can add auth data	8	add_authdata
30	Can change auth data	8	change_authdata
31	Can delete auth data	8	delete_authdata
32	Can view auth data	8	view_authdata
33	Can add storage entry	9	add_storageentry
34	Can change storage entry	9	change_storageentry
35	Can delete storage entry	9	delete_storageentry
36	Can view storage entry	9	view_storageentry
37	Can add outstanding token	10	add_outstandingtoken
38	Can change outstanding token	10	change_outstandingtoken
39	Can delete outstanding token	10	delete_outstandingtoken
40	Can view outstanding token	10	view_outstandingtoken
41	Can add denylisted token	11	add_denylistedtoken
42	Can change denylisted token	11	change_denylistedtoken
43	Can delete denylisted token	11	delete_denylistedtoken
44	Can view denylisted token	11	view_denylistedtoken
45	Can add application	12	add_application
46	Can change application	12	change_application
47	Can delete application	12	delete_application
48	Can view application	12	view_application
49	Can add application token	13	add_applicationtoken
50	Can change application token	13	change_applicationtoken
51	Can delete application token	13	delete_applicationtoken
52	Can view application token	13	view_applicationtoken
53	Can add membership	14	add_membership
54	Can change membership	14	change_membership
55	Can delete membership	14	delete_membership
56	Can view membership	14	view_membership
57	Can add project	15	add_project
58	Can change project	15	change_project
59	Can delete project	15	delete_project
60	Can view project	15	view_project
61	Can add issue status	25	add_issuestatus
62	Can change issue status	25	change_issuestatus
63	Can delete issue status	25	delete_issuestatus
64	Can view issue status	25	view_issuestatus
65	Can add issue type	26	add_issuetype
66	Can change issue type	26	change_issuetype
67	Can delete issue type	26	delete_issuetype
68	Can view issue type	26	view_issuetype
69	Can add points	19	add_points
70	Can change points	19	change_points
71	Can delete points	19	delete_points
72	Can view points	19	view_points
73	Can add priority	23	add_priority
74	Can change priority	23	change_priority
75	Can delete priority	23	delete_priority
76	Can view priority	23	view_priority
77	Can add project template	30	add_projecttemplate
78	Can change project template	30	change_projecttemplate
79	Can delete project template	30	delete_projecttemplate
80	Can view project template	30	view_projecttemplate
81	Can add severity	24	add_severity
82	Can change severity	24	change_severity
83	Can delete severity	24	delete_severity
84	Can view severity	24	view_severity
85	Can add task status	21	add_taskstatus
86	Can change task status	21	change_taskstatus
87	Can delete task status	21	delete_taskstatus
88	Can view task status	21	view_taskstatus
89	Can add user story status	18	add_userstorystatus
90	Can change user story status	18	change_userstorystatus
91	Can delete user story status	18	delete_userstorystatus
92	Can view user story status	18	view_userstorystatus
93	Can add project modules config	16	add_projectmodulesconfig
94	Can change project modules config	16	change_projectmodulesconfig
95	Can delete project modules config	16	delete_projectmodulesconfig
96	Can view project modules config	16	view_projectmodulesconfig
97	Can add epic status	17	add_epicstatus
98	Can change epic status	17	change_epicstatus
99	Can delete epic status	17	delete_epicstatus
100	Can view epic status	17	view_epicstatus
101	Can add issue due date	27	add_issueduedate
102	Can change issue due date	27	change_issueduedate
103	Can delete issue due date	27	delete_issueduedate
104	Can view issue due date	27	view_issueduedate
105	Can add task due date	22	add_taskduedate
106	Can change task due date	22	change_taskduedate
107	Can delete task due date	22	delete_taskduedate
108	Can view task due date	22	view_taskduedate
109	Can add user story due date	20	add_userstoryduedate
110	Can change user story due date	20	change_userstoryduedate
111	Can delete user story due date	20	delete_userstoryduedate
112	Can view user story due date	20	view_userstoryduedate
113	Can add swimlane	28	add_swimlane
114	Can change swimlane	28	change_swimlane
115	Can delete swimlane	28	delete_swimlane
116	Can view swimlane	28	view_swimlane
117	Can add swimlane user story status	29	add_swimlaneuserstorystatus
118	Can change swimlane user story status	29	change_swimlaneuserstorystatus
119	Can delete swimlane user story status	29	delete_swimlaneuserstorystatus
120	Can view swimlane user story status	29	view_swimlaneuserstorystatus
121	Can add reference	31	add_reference
122	Can change reference	31	change_reference
123	Can delete reference	31	delete_reference
124	Can view reference	31	view_reference
125	Can add issue custom attribute	35	add_issuecustomattribute
126	Can change issue custom attribute	35	change_issuecustomattribute
127	Can delete issue custom attribute	35	delete_issuecustomattribute
128	Can view issue custom attribute	35	view_issuecustomattribute
129	Can add task custom attribute	34	add_taskcustomattribute
130	Can change task custom attribute	34	change_taskcustomattribute
131	Can delete task custom attribute	34	delete_taskcustomattribute
132	Can view task custom attribute	34	view_taskcustomattribute
133	Can add user story custom attribute	33	add_userstorycustomattribute
134	Can change user story custom attribute	33	change_userstorycustomattribute
135	Can delete user story custom attribute	33	delete_userstorycustomattribute
136	Can view user story custom attribute	33	view_userstorycustomattribute
137	Can add issue custom attributes values	39	add_issuecustomattributesvalues
138	Can change issue custom attributes values	39	change_issuecustomattributesvalues
139	Can delete issue custom attributes values	39	delete_issuecustomattributesvalues
140	Can view issue custom attributes values	39	view_issuecustomattributesvalues
141	Can add task custom attributes values	38	add_taskcustomattributesvalues
142	Can change task custom attributes values	38	change_taskcustomattributesvalues
143	Can delete task custom attributes values	38	delete_taskcustomattributesvalues
144	Can view task custom attributes values	38	view_taskcustomattributesvalues
145	Can add user story custom attributes values	37	add_userstorycustomattributesvalues
146	Can change user story custom attributes values	37	change_userstorycustomattributesvalues
147	Can delete user story custom attributes values	37	delete_userstorycustomattributesvalues
148	Can view user story custom attributes values	37	view_userstorycustomattributesvalues
149	Can add epic custom attribute	32	add_epiccustomattribute
150	Can change epic custom attribute	32	change_epiccustomattribute
151	Can delete epic custom attribute	32	delete_epiccustomattribute
152	Can view epic custom attribute	32	view_epiccustomattribute
153	Can add epic custom attributes values	36	add_epiccustomattributesvalues
154	Can change epic custom attributes values	36	change_epiccustomattributesvalues
155	Can delete epic custom attributes values	36	delete_epiccustomattributesvalues
156	Can view epic custom attributes values	36	view_epiccustomattributesvalues
157	Can add history entry	40	add_historyentry
158	Can change history entry	40	change_historyentry
159	Can delete history entry	40	delete_historyentry
160	Can view history entry	40	view_historyentry
161	Can add notify policy	41	add_notifypolicy
162	Can change notify policy	41	change_notifypolicy
163	Can delete notify policy	41	delete_notifypolicy
164	Can view notify policy	41	view_notifypolicy
165	Can add history change notification	42	add_historychangenotification
166	Can change history change notification	42	change_historychangenotification
167	Can delete history change notification	42	delete_historychangenotification
168	Can view history change notification	42	view_historychangenotification
169	Can add Watched	43	add_watched
170	Can change Watched	43	change_watched
171	Can delete Watched	43	delete_watched
172	Can view Watched	43	view_watched
173	Can add web notification	44	add_webnotification
174	Can change web notification	44	change_webnotification
175	Can delete web notification	44	delete_webnotification
176	Can view web notification	44	view_webnotification
177	Can add attachment	45	add_attachment
178	Can change attachment	45	change_attachment
179	Can delete attachment	45	delete_attachment
180	Can view attachment	45	view_attachment
181	Can add Like	46	add_like
182	Can change Like	46	change_like
183	Can delete Like	46	delete_like
184	Can view Like	46	view_like
185	Can add Vote	48	add_vote
186	Can change Vote	48	change_vote
187	Can delete Vote	48	delete_vote
188	Can view Vote	48	view_vote
189	Can add Votes	47	add_votes
190	Can change Votes	47	change_votes
191	Can delete Votes	47	delete_votes
192	Can view Votes	47	view_votes
193	Can add milestone	49	add_milestone
194	Can change milestone	49	change_milestone
195	Can delete milestone	49	delete_milestone
196	Can view milestone	49	view_milestone
197	Can add epic	50	add_epic
198	Can change epic	50	change_epic
199	Can delete epic	50	delete_epic
200	Can view epic	50	view_epic
201	Can add related user story	51	add_relateduserstory
202	Can change related user story	51	change_relateduserstory
203	Can delete related user story	51	delete_relateduserstory
204	Can view related user story	51	view_relateduserstory
205	Can add role points	52	add_rolepoints
206	Can change role points	52	change_rolepoints
207	Can delete role points	52	delete_rolepoints
208	Can view role points	52	view_rolepoints
209	Can add user story	53	add_userstory
210	Can change user story	53	change_userstory
211	Can delete user story	53	delete_userstory
212	Can view user story	53	view_userstory
213	Can add task	54	add_task
214	Can change task	54	change_task
215	Can delete task	54	delete_task
216	Can view task	54	view_task
217	Can add issue	55	add_issue
218	Can change issue	55	change_issue
219	Can delete issue	55	delete_issue
220	Can view issue	55	view_issue
221	Can add wiki link	57	add_wikilink
222	Can change wiki link	57	change_wikilink
223	Can delete wiki link	57	delete_wikilink
224	Can view wiki link	57	view_wikilink
225	Can add wiki page	56	add_wikipage
226	Can change wiki page	56	change_wikipage
227	Can delete wiki page	56	delete_wikipage
228	Can view wiki page	56	view_wikipage
229	Can add contact entry	58	add_contactentry
230	Can change contact entry	58	change_contactentry
231	Can delete contact entry	58	delete_contactentry
232	Can view contact entry	58	view_contactentry
233	Can add user project settings	59	add_userprojectsettings
234	Can change user project settings	59	change_userprojectsettings
235	Can delete user project settings	59	delete_userprojectsettings
236	Can view user project settings	59	view_userprojectsettings
237	Can add timeline	60	add_timeline
238	Can change timeline	60	change_timeline
239	Can delete timeline	60	delete_timeline
240	Can view timeline	60	view_timeline
241	Can add feedback entry	61	add_feedbackentry
242	Can change feedback entry	61	change_feedbackentry
243	Can delete feedback entry	61	delete_feedbackentry
244	Can view feedback entry	61	view_feedbackentry
245	Can add webhook	62	add_webhook
246	Can change webhook	62	change_webhook
247	Can delete webhook	62	delete_webhook
248	Can view webhook	62	view_webhook
249	Can add webhook log	63	add_webhooklog
250	Can change webhook log	63	change_webhooklog
251	Can delete webhook log	63	delete_webhooklog
252	Can view webhook log	63	view_webhooklog
253	Can add Message	64	add_message
254	Can change Message	64	change_message
255	Can delete Message	64	delete_message
256	Can view Message	64	view_message
257	Can add source	65	add_source
258	Can change source	65	change_source
259	Can delete source	65	delete_source
260	Can view source	65	view_source
261	Can add thumbnail	66	add_thumbnail
262	Can change thumbnail	66	change_thumbnail
263	Can delete thumbnail	66	delete_thumbnail
264	Can view thumbnail	66	view_thumbnail
265	Can add thumbnail dimensions	67	add_thumbnaildimensions
266	Can change thumbnail dimensions	67	change_thumbnaildimensions
267	Can delete thumbnail dimensions	67	delete_thumbnaildimensions
268	Can view thumbnail dimensions	67	view_thumbnaildimensions
269	Can add instance telemetry	68	add_instancetelemetry
270	Can change instance telemetry	68	change_instancetelemetry
271	Can delete instance telemetry	68	delete_instancetelemetry
272	Can view instance telemetry	68	view_instancetelemetry
\.


--
-- Data for Name: contact_contactentry; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.contact_contactentry (id, comment, created_date, project_id, user_id) FROM stdin;
\.


--
-- Data for Name: custom_attributes_epiccustomattribute; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_epiccustomattribute (id, name, description, type, "order", created_date, modified_date, project_id, extra) FROM stdin;
\.


--
-- Data for Name: custom_attributes_epiccustomattributesvalues; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_epiccustomattributesvalues (id, version, attributes_values, epic_id) FROM stdin;
\.


--
-- Data for Name: custom_attributes_issuecustomattribute; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_issuecustomattribute (id, name, description, "order", created_date, modified_date, project_id, type, extra) FROM stdin;
\.


--
-- Data for Name: custom_attributes_issuecustomattributesvalues; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_issuecustomattributesvalues (id, version, attributes_values, issue_id) FROM stdin;
\.


--
-- Data for Name: custom_attributes_taskcustomattribute; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_taskcustomattribute (id, name, description, "order", created_date, modified_date, project_id, type, extra) FROM stdin;
\.


--
-- Data for Name: custom_attributes_taskcustomattributesvalues; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_taskcustomattributesvalues (id, version, attributes_values, task_id) FROM stdin;
\.


--
-- Data for Name: custom_attributes_userstorycustomattribute; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_userstorycustomattribute (id, name, description, "order", created_date, modified_date, project_id, type, extra) FROM stdin;
\.


--
-- Data for Name: custom_attributes_userstorycustomattributesvalues; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.custom_attributes_userstorycustomattributesvalues (id, version, attributes_values, user_story_id) FROM stdin;
\.


--
-- Data for Name: django_admin_log; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.django_admin_log (id, action_time, object_id, object_repr, action_flag, change_message, content_type_id, user_id) FROM stdin;
\.


--
-- Data for Name: django_content_type; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.django_content_type (id, app_label, model) FROM stdin;
1	auth	permission
2	auth	group
3	contenttypes	contenttype
4	sessions	session
5	admin	logentry
6	users	user
7	users	role
8	users	authdata
9	userstorage	storageentry
10	token_denylist	outstandingtoken
11	token_denylist	denylistedtoken
12	external_apps	application
13	external_apps	applicationtoken
14	projects	membership
15	projects	project
16	projects	projectmodulesconfig
17	projects	epicstatus
18	projects	userstorystatus
19	projects	points
20	projects	userstoryduedate
21	projects	taskstatus
22	projects	taskduedate
23	projects	priority
24	projects	severity
25	projects	issuestatus
26	projects	issuetype
27	projects	issueduedate
28	projects	swimlane
29	projects	swimlaneuserstorystatus
30	projects	projecttemplate
31	references	reference
32	custom_attributes	epiccustomattribute
33	custom_attributes	userstorycustomattribute
34	custom_attributes	taskcustomattribute
35	custom_attributes	issuecustomattribute
36	custom_attributes	epiccustomattributesvalues
37	custom_attributes	userstorycustomattributesvalues
38	custom_attributes	taskcustomattributesvalues
39	custom_attributes	issuecustomattributesvalues
40	history	historyentry
41	notifications	notifypolicy
42	notifications	historychangenotification
43	notifications	watched
44	notifications	webnotification
45	attachments	attachment
46	likes	like
47	votes	votes
48	votes	vote
49	milestones	milestone
50	epics	epic
51	epics	relateduserstory
52	userstories	rolepoints
53	userstories	userstory
54	tasks	task
55	issues	issue
56	wiki	wikipage
57	wiki	wikilink
58	contact	contactentry
59	settings	userprojectsettings
60	timeline	timeline
61	feedback	feedbackentry
62	webhooks	webhook
63	webhooks	webhooklog
64	djmail	message
65	easy_thumbnails	source
66	easy_thumbnails	thumbnail
67	easy_thumbnails	thumbnaildimensions
68	telemetry	instancetelemetry
\.


--
-- Data for Name: django_migrations; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.django_migrations (id, app, name, applied) FROM stdin;
1	contenttypes	0001_initial	2023-02-06 15:01:21.723181+01
2	users	0001_initial	2023-02-06 15:01:21.788235+01
3	admin	0001_initial	2023-02-06 15:01:21.876323+01
4	admin	0002_logentry_remove_auto_add	2023-02-06 15:01:21.943379+01
5	admin	0003_logentry_add_action_flag_choices	2023-02-06 15:01:21.969583+01
6	users	0002_auto_20140903_0916	2023-02-06 15:01:22.010919+01
7	projects	0001_initial	2023-02-06 15:01:22.202365+01
8	projects	0002_auto_20140903_0920	2023-02-06 15:01:23.037313+01
9	attachments	0001_initial	2023-02-06 15:01:23.204583+01
10	attachments	0002_add_size_and_name_fields	2023-02-06 15:01:23.353076+01
11	attachments	0003_auto_20150114_0954	2023-02-06 15:01:23.369726+01
12	attachments	0004_auto_20150508_1141	2023-02-06 15:01:23.400946+01
13	attachments	0005_attachment_sha1	2023-02-06 15:01:23.420314+01
14	attachments	0006_auto_20160617_1233	2023-02-06 15:01:23.448713+01
15	attachments	0007_attachment_from_comment	2023-02-06 15:01:23.47097+01
16	attachments	0008_auto_20170201_1053	2023-02-06 15:01:23.566351+01
17	contenttypes	0002_remove_content_type_name	2023-02-06 15:01:23.607858+01
18	auth	0001_initial	2023-02-06 15:01:23.759675+01
19	auth	0002_alter_permission_name_max_length	2023-02-06 15:01:23.922384+01
20	auth	0003_alter_user_email_max_length	2023-02-06 15:01:23.941792+01
21	auth	0004_alter_user_username_opts	2023-02-06 15:01:23.954676+01
22	auth	0005_alter_user_last_login_null	2023-02-06 15:01:23.967914+01
23	auth	0006_require_contenttypes_0002	2023-02-06 15:01:23.975844+01
24	auth	0007_alter_validators_add_error_messages	2023-02-06 15:01:23.989714+01
25	auth	0008_alter_user_username_max_length	2023-02-06 15:01:24.003873+01
26	auth	0009_alter_user_last_name_max_length	2023-02-06 15:01:24.01817+01
27	auth	0010_alter_group_name_max_length	2023-02-06 15:01:24.036212+01
28	auth	0011_update_proxy_permissions	2023-02-06 15:01:24.065583+01
29	users	0003_auto_20140903_0925	2023-02-06 15:01:24.116301+01
30	users	0004_auto_20140913_1914	2023-02-06 15:01:24.21713+01
31	users	0005_alter_user_photo	2023-02-06 15:01:24.240873+01
32	users	0006_auto_20141030_1132	2023-02-06 15:01:24.273488+01
33	bitbucket	0001_initial	2023-02-06 15:01:24.302944+01
34	milestones	0001_initial	2023-02-06 15:01:24.460352+01
35	issues	0001_initial	2023-02-06 15:01:24.751104+01
36	userstories	0001_initial	2023-02-06 15:01:25.275118+01
37	userstories	0002_auto_20140903_1301	2023-02-06 15:01:25.563092+01
38	userstories	0003_userstory_order_fields	2023-02-06 15:01:25.950493+01
39	userstories	0004_auto_20141001_1817	2023-02-06 15:01:25.988582+01
40	userstories	0005_auto_20141009_1656	2023-02-06 15:01:26.021214+01
41	userstories	0006_auto_20141014_1524	2023-02-06 15:01:26.074153+01
42	userstories	0007_userstory_external_reference	2023-02-06 15:01:26.098951+01
43	userstories	0008_auto_20141210_1107	2023-02-06 15:01:26.126609+01
44	userstories	0009_remove_userstory_is_archived	2023-02-06 15:01:26.165305+01
45	projects	0003_auto_20140913_1710	2023-02-06 15:01:26.218591+01
46	projects	0004_auto_20141002_2337	2023-02-06 15:01:26.257599+01
47	projects	0005_membership_invitation_extra_text	2023-02-06 15:01:26.327937+01
48	notifications	0001_initial	2023-02-06 15:01:26.415338+01
49	history	0001_initial	2023-02-06 15:01:26.514146+01
50	history	0002_auto_20140916_0936	2023-02-06 15:01:26.584909+01
51	history	0003_auto_20140917_1405	2023-02-06 15:01:26.686078+01
52	history	0004_historyentry_is_hidden	2023-02-06 15:01:26.713192+01
53	notifications	0002_historychangenotification	2023-02-06 15:01:26.89071+01
54	notifications	0003_auto_20141029_1143	2023-02-06 15:01:27.165255+01
55	notifications	0004_watched	2023-02-06 15:01:27.275479+01
56	userstories	0010_remove_userstory_watchers	2023-02-06 15:01:27.457212+01
57	userstories	0011_userstory_tribe_gig	2023-02-06 15:01:27.503513+01
58	tasks	0001_initial	2023-02-06 15:01:27.569936+01
59	tasks	0002_tasks_order_fields	2023-02-06 15:01:27.855206+01
60	tasks	0003_task_external_reference	2023-02-06 15:01:27.887812+01
61	tasks	0004_auto_20141210_1107	2023-02-06 15:01:27.918661+01
62	tasks	0005_auto_20150114_0954	2023-02-06 15:01:27.9457+01
63	tasks	0006_auto_20150623_1923	2023-02-06 15:01:28.013567+01
64	tasks	0007_auto_20150629_1556	2023-02-06 15:01:28.049827+01
65	tasks	0008_remove_task_watchers	2023-02-06 15:01:28.245753+01
66	tasks	0009_auto_20151104_1131	2023-02-06 15:01:28.283472+01
67	users	0007_auto_20150209_1611	2023-02-06 15:01:28.411464+01
68	users	0008_auto_20150213_1701	2023-02-06 15:01:28.526121+01
69	users	0009_auto_20150326_1241	2023-02-06 15:01:28.612667+01
70	users	0010_auto_20150414_0936	2023-02-06 15:01:28.650087+01
71	timeline	0001_initial	2023-02-06 15:01:28.741271+01
72	projects	0006_auto_20141029_1040	2023-02-06 15:01:28.937742+01
73	projects	0007_auto_20141024_1011	2023-02-06 15:01:29.032768+01
74	projects	0008_auto_20141024_1012	2023-02-06 15:01:29.317833+01
75	projects	0009_auto_20141024_1037	2023-02-06 15:01:29.525734+01
76	projects	0010_project_modules_config	2023-02-06 15:01:29.570883+01
77	projects	0011_auto_20141028_2057	2023-02-06 15:01:29.688206+01
78	projects	0012_auto_20141210_1009	2023-02-06 15:01:29.72645+01
79	projects	0013_auto_20141210_1040	2023-02-06 15:01:29.776453+01
80	projects	0014_userstorystatus_is_archived	2023-02-06 15:01:29.805079+01
81	projects	0015_auto_20141230_1212	2023-02-06 15:01:29.838823+01
82	projects	0016_fix_json_field_not_null	2023-02-06 15:01:29.871962+01
83	projects	0017_fix_is_private_for_projects	2023-02-06 15:01:29.925379+01
84	projects	0018_auto_20150219_1606	2023-02-06 15:01:30.105315+01
85	projects	0019_auto_20150311_0821	2023-02-06 15:01:30.309898+01
86	timeline	0002_auto_20150327_1056	2023-02-06 15:01:30.464286+01
87	timeline	0003_auto_20150410_0829	2023-02-06 15:01:30.685132+01
88	timeline	0004_auto_20150603_1312	2023-02-06 15:01:30.728997+01
89	projects	0020_membership_user_order	2023-02-06 15:01:30.760361+01
90	projects	0021_auto_20150504_1524	2023-02-06 15:01:30.811265+01
91	projects	0022_auto_20150701_0924	2023-02-06 15:01:30.885957+01
92	projects	0023_auto_20150721_1511	2023-02-06 15:01:30.91898+01
93	projects	0024_auto_20150810_1247	2023-02-06 15:01:30.975904+01
94	projects	0025_auto_20150901_1600	2023-02-06 15:01:31.101803+01
95	projects	0026_auto_20150911_1237	2023-02-06 15:01:31.137228+01
96	projects	0027_auto_20150916_1302	2023-02-06 15:01:31.187718+01
97	projects	0028_project_is_featured	2023-02-06 15:01:31.216368+01
98	projects	0029_project_is_looking_for_people	2023-02-06 15:01:31.242127+01
99	likes	0001_initial	2023-02-06 15:01:31.383692+01
100	projects	0030_auto_20151128_0757	2023-02-06 15:01:31.793965+01
101	projects	0031_project_logo	2023-02-06 15:01:32.006131+01
102	projects	0032_auto_20151202_1151	2023-02-06 15:01:32.090841+01
103	projects	0033_text_search_indexes	2023-02-06 15:01:32.10396+01
104	projects	0034_project_looking_for_people_note	2023-02-06 15:01:32.135174+01
105	projects	0035_project_blocked_code	2023-02-06 15:01:32.18062+01
106	projects	0036_project_transfer_token	2023-02-06 15:01:32.226989+01
107	projects	0037_auto_20160208_1751	2023-02-06 15:01:32.267999+01
108	projects	0038_auto_20160215_1133	2023-02-06 15:01:32.31357+01
109	projects	0039_auto_20160322_1157	2023-02-06 15:01:32.354185+01
110	projects	0040_remove_memberships_of_cancelled_users_acounts	2023-02-06 15:01:32.386992+01
111	projects	0043_auto_20160530_1004	2023-02-06 15:01:32.446697+01
112	projects	0044_auto_20160531_1150	2023-02-06 15:01:32.476025+01
113	projects	0041_auto_20160519_1058	2023-02-06 15:01:32.502657+01
114	projects	0042_auto_20160525_0911	2023-02-06 15:01:32.635158+01
115	projects	0045_merge	2023-02-06 15:01:32.643431+01
116	issues	0002_issue_external_reference	2023-02-06 15:01:32.678475+01
117	issues	0003_auto_20141210_1108	2023-02-06 15:01:32.715358+01
118	issues	0004_auto_20150114_0954	2023-02-06 15:01:32.749618+01
119	issues	0005_auto_20150623_1923	2023-02-06 15:01:32.88844+01
120	issues	0006_remove_issue_watchers	2023-02-06 15:01:33.001765+01
121	projects	0046_triggers_to_update_tags_colors	2023-02-06 15:01:33.033964+01
122	projects	0047_auto_20160614_1201	2023-02-06 15:01:33.12178+01
123	projects	0048_auto_20160615_1508	2023-02-06 15:01:33.142294+01
124	projects	0049_auto_20160629_1443	2023-02-06 15:01:33.576502+01
125	projects	0050_project_epics_csv_uuid	2023-02-06 15:01:33.731586+01
126	projects	0051_auto_20160729_0802	2023-02-06 15:01:33.809985+01
127	projects	0052_epic_status	2023-02-06 15:01:33.879778+01
128	projects	0053_auto_20160927_0741	2023-02-06 15:01:33.91437+01
129	projects	0054_auto_20160928_0540	2023-02-06 15:01:34.157708+01
130	projects	0055_json_to_jsonb	2023-02-06 15:01:34.339233+01
131	projects	0056_auto_20161110_1518	2023-02-06 15:01:34.408988+01
132	contact	0001_initial	2023-02-06 15:01:34.468716+01
133	userstories	0012_auto_20160614_1201	2023-02-06 15:01:34.554147+01
134	wiki	0001_initial	2023-02-06 15:01:34.857762+01
135	wiki	0002_remove_wikipage_watchers	2023-02-06 15:01:35.245853+01
136	wiki	0003_auto_20160615_0721	2023-02-06 15:01:35.290213+01
137	users	0011_user_theme	2023-02-06 15:01:35.32442+01
138	users	0012_auto_20150812_1142	2023-02-06 15:01:35.350371+01
139	users	0013_auto_20150901_1600	2023-02-06 15:01:35.388658+01
140	users	0014_auto_20151005_1357	2023-02-06 15:01:35.4382+01
141	users	0015_auto_20160120_1409	2023-02-06 15:01:35.472895+01
142	users	0016_auto_20160204_1050	2023-02-06 15:01:35.509473+01
143	users	0017_auto_20160208_1751	2023-02-06 15:01:35.546586+01
144	users	0018_remove_vote_issues_in_roles_permissions_field	2023-02-06 15:01:35.55625+01
145	users	0019_auto_20160519_1058	2023-02-06 15:01:35.583591+01
146	users	0020_auto_20160525_1229	2023-02-06 15:01:35.5969+01
147	users	0021_auto_20160614_1201	2023-02-06 15:01:35.624534+01
148	users	0022_auto_20160629_1443	2023-02-06 15:01:35.77961+01
149	history	0005_auto_20141120_1119	2023-02-06 15:01:35.82162+01
150	history	0006_fix_json_field_not_null	2023-02-06 15:01:35.8548+01
151	history	0007_set_bloked_note_and_is_blocked_in_snapshots	2023-02-06 15:01:35.893373+01
152	history	0008_auto_20150508_1028	2023-02-06 15:01:35.912379+01
153	history	0009_auto_20160512_1110	2023-02-06 15:01:35.930696+01
154	history	0010_historyentry_project	2023-02-06 15:01:35.959171+01
155	history	0011_auto_20160629_1036	2023-02-06 15:01:36.231738+01
156	history	0012_auto_20160629_1036	2023-02-06 15:01:36.286069+01
157	epics	0001_initial	2023-02-06 15:01:36.441228+01
158	epics	0002_epic_color	2023-02-06 15:01:36.570437+01
159	custom_attributes	0001_initial	2023-02-06 15:01:37.025415+01
160	custom_attributes	0002_issuecustomattributesvalues_taskcustomattributesvalues_userstorycustomattributesvalues	2023-02-06 15:01:37.367336+01
161	custom_attributes	0003_triggers_on_delete_customattribute	2023-02-06 15:01:37.422552+01
162	custom_attributes	0004_create_empty_customattributesvalues_for_existen_object	2023-02-06 15:01:37.547442+01
163	custom_attributes	0005_auto_20150505_1639	2023-02-06 15:01:37.607814+01
164	custom_attributes	0006_auto_20151014_1645	2023-02-06 15:01:37.67356+01
165	custom_attributes	0007_auto_20160208_1751	2023-02-06 15:01:37.74708+01
166	custom_attributes	0008_auto_20160728_0540	2023-02-06 15:01:37.972755+01
167	custom_attributes	0009_auto_20160728_1002	2023-02-06 15:01:38.204464+01
168	custom_attributes	0010_auto_20160928_0540	2023-02-06 15:01:38.601987+01
169	custom_attributes	0011_json_to_jsonb	2023-02-06 15:01:38.446567+01
170	custom_attributes	0012_auto_20161201_1628	2023-02-06 15:01:38.526431+01
171	custom_attributes	0013_auto_20181022_1624	2023-02-06 15:01:38.653407+01
172	custom_attributes	0014_auto_20181025_0711	2023-02-06 15:01:38.861621+01
173	custom_attributes	0015_auto_20200615_0811	2023-02-06 15:01:38.879114+01
174	djmail	0001_initial	2023-02-06 15:01:38.915656+01
175	djmail	0002_auto_20161118_1347	2023-02-06 15:01:38.952933+01
176	easy_thumbnails	0001_initial	2023-02-06 15:01:39.074932+01
177	easy_thumbnails	0002_thumbnaildimensions	2023-02-06 15:01:39.312632+01
178	epics	0003_auto_20160901_1021	2023-02-06 15:01:39.379562+01
179	epics	0004_auto_20160928_0540	2023-02-06 15:01:39.608126+01
180	epics	0005_epic_external_reference	2023-02-06 15:01:39.656905+01
181	epics	0006_auto_20200615_0811	2023-02-06 15:01:39.759029+01
182	external_apps	0001_initial	2023-02-06 15:01:39.938119+01
183	external_apps	0002_remove_application_key	2023-02-06 15:01:40.022647+01
184	external_apps	0003_auto_20170607_2320	2023-02-06 15:01:40.078316+01
185	external_apps	0004_typo_fix	2023-02-06 15:01:40.120419+01
186	feedback	0001_initial	2023-02-06 15:01:40.155435+01
187	github	0001_initial	2023-02-06 15:01:40.232861+01
188	gitlab	0001_initial	2023-02-06 15:01:40.278198+01
189	gitlab	0002_auto_20150703_1102	2023-02-06 15:01:40.46733+01
190	gogs	0001_initial	2023-02-06 15:01:40.523576+01
191	history	0013_historyentry_values_diff_cache	2023-02-06 15:01:40.555841+01
192	history	0014_json_to_jsonb	2023-02-06 15:01:40.692647+01
193	issues	0007_auto_20160614_1201	2023-02-06 15:01:40.777573+01
194	issues	0008_add_due_date	2023-02-06 15:01:40.82471+01
195	issues	0009_auto_20200615_0811	2023-02-06 15:01:40.933986+01
196	likes	0002_auto_20151130_2230	2023-02-06 15:01:40.989376+01
197	milestones	0002_remove_milestone_watchers	2023-02-06 15:01:41.088456+01
198	milestones	0003_auto_20200615_0811	2023-02-06 15:01:41.118996+01
199	notifications	0005_auto_20151005_1357	2023-02-06 15:01:41.357244+01
200	notifications	0006_auto_20151103_0954	2023-02-06 15:01:41.388827+01
201	notifications	0007_notifypolicy_live_notify_level	2023-02-06 15:01:41.423634+01
202	notifications	0008_auto_20181010_1124	2023-02-06 15:01:41.531129+01
203	notifications	0009_auto_20200615_0811	2023-02-06 15:01:41.6242+01
204	projects	0057_auto_20161129_0945	2023-02-06 15:01:41.635679+01
205	projects	0058_auto_20161215_1347	2023-02-06 15:01:41.699734+01
206	projects	0059_auto_20170116_1633	2023-02-06 15:01:41.73443+01
207	projects	0060_auto_20180614_1338	2023-02-06 15:01:41.989483+01
208	projects	0061_auto_20180918_1355	2023-02-06 15:01:42.06659+01
209	projects	0062_auto_20190826_0920	2023-02-06 15:01:42.24296+01
210	projects	0063_auto_20200615_0811	2023-02-06 15:01:42.516822+01
211	projects	0064_swimlane	2023-02-06 15:01:42.561687+01
212	projects	0065_swimlaneuserstorystatus	2023-02-06 15:01:42.697716+01
213	projects	0066_project_default_swimlane	2023-02-06 15:01:42.846952+01
214	projects	0067_auto_20201230_1237	2023-02-06 15:01:42.881531+01
215	references	0001_initial	2023-02-06 15:01:43.117898+01
216	sessions	0001_initial	2023-02-06 15:01:43.189087+01
217	settings	0001_initial	2023-02-06 15:01:43.364117+01
218	tasks	0010_auto_20160614_1201	2023-02-06 15:01:43.475107+01
219	tasks	0011_auto_20160928_0755	2023-02-06 15:01:43.897564+01
220	tasks	0012_add_due_date	2023-02-06 15:01:43.986561+01
221	tasks	0013_auto_20200615_0811	2023-02-06 15:01:44.079955+01
222	telemetry	0001_initial	2023-02-06 15:01:44.100863+01
223	timeline	0005_auto_20160706_0723	2023-02-06 15:01:44.30279+01
224	timeline	0006_json_to_jsonb	2023-02-06 15:01:44.477183+01
225	timeline	0007_auto_20170406_0615	2023-02-06 15:01:44.547565+01
226	timeline	0008_auto_20190606_1528	2023-02-06 15:01:44.700576+01
227	token_denylist	0001_initial	2023-02-06 15:01:44.830746+01
228	users	0023_json_to_jsonb	2023-02-06 15:01:45.039379+01
229	users	0024_auto_20170406_0727	2023-02-06 15:01:45.078063+01
230	users	0025_user_uuid	2023-02-06 15:01:45.201588+01
231	users	0026_auto_20180514_1513	2023-02-06 15:01:45.266675+01
232	users	0027_auto_20180610_2011	2023-02-06 15:01:45.295586+01
233	users	0028_auto_20200615_0811	2023-02-06 15:01:45.324795+01
234	users	0029_user_verified_email	2023-02-06 15:01:45.364396+01
235	users	0030_auto_20201119_1031	2023-02-06 15:01:45.562973+01
236	users	0031_auto_20210108_1430	2023-02-06 15:01:45.658701+01
237	users	0032_user_date_cancelled	2023-02-06 15:01:45.68722+01
238	users	0033_auto_20211110_1526	2023-02-06 15:01:45.730937+01
239	userstorage	0001_initial	2023-02-06 15:01:45.832178+01
240	userstorage	0002_fix_json_field_not_null	2023-02-06 15:01:45.854334+01
241	userstorage	0003_json_to_jsonb	2023-02-06 15:01:45.940688+01
242	userstories	0013_auto_20160722_1018	2023-02-06 15:01:45.982988+01
243	userstories	0014_auto_20160928_0540	2023-02-06 15:01:46.588041+01
244	userstories	0015_add_due_date	2023-02-06 15:01:46.667986+01
245	userstories	0016_userstory_assigned_users	2023-02-06 15:01:46.883749+01
246	userstories	0017_userstory_generated_from_task	2023-02-06 15:01:47.035631+01
247	userstories	0018_auto_20200615_0811	2023-02-06 15:01:47.173352+01
248	userstories	0019_userstory_from_task_ref	2023-02-06 15:01:47.212434+01
249	userstories	0020_userstory_swimlane	2023-02-06 15:01:47.26213+01
250	userstories	0021_auto_20201202_0850	2023-02-06 15:01:47.401509+01
251	votes	0001_initial	2023-02-06 15:01:47.722933+01
252	votes	0002_auto_20150805_1600	2023-02-06 15:01:47.925033+01
253	webhooks	0001_initial	2023-02-06 15:01:48.054672+01
254	webhooks	0002_webhook_name	2023-02-06 15:01:48.154149+01
255	webhooks	0003_auto_20150122_1021	2023-02-06 15:01:48.199492+01
256	webhooks	0004_auto_20150202_0834	2023-02-06 15:01:48.232183+01
257	webhooks	0005_auto_20150505_1639	2023-02-06 15:01:48.283274+01
258	webhooks	0006_json_to_jsonb	2023-02-06 15:01:48.350279+01
259	wiki	0004_auto_20160928_0540	2023-02-06 15:01:48.499035+01
260	wiki	0005_auto_20161201_1628	2023-02-06 15:01:48.538138+01
\.


--
-- Data for Name: django_session; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.django_session (session_key, session_data, expire_date) FROM stdin;
\.


--
-- Data for Name: djmail_message; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.djmail_message (uuid, from_email, to_email, body_text, body_html, subject, data, retry_count, status, priority, created_at, sent_at, exception) FROM stdin;
\.


--
-- Data for Name: easy_thumbnails_source; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.easy_thumbnails_source (id, storage_hash, name, modified) FROM stdin;
\.


--
-- Data for Name: easy_thumbnails_thumbnail; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.easy_thumbnails_thumbnail (id, storage_hash, name, modified, source_id) FROM stdin;
\.


--
-- Data for Name: easy_thumbnails_thumbnaildimensions; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.easy_thumbnails_thumbnaildimensions (id, thumbnail_id, width, height) FROM stdin;
\.


--
-- Data for Name: epics_epic; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.epics_epic (id, tags, version, is_blocked, blocked_note, ref, epics_order, created_date, modified_date, subject, description, client_requirement, team_requirement, assigned_to_id, owner_id, project_id, status_id, color, external_reference) FROM stdin;
\.


--
-- Data for Name: epics_relateduserstory; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.epics_relateduserstory (id, "order", epic_id, user_story_id) FROM stdin;
\.


--
-- Data for Name: external_apps_application; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.external_apps_application (id, name, icon_url, web, description, next_url) FROM stdin;
\.


--
-- Data for Name: external_apps_applicationtoken; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.external_apps_applicationtoken (id, auth_code, token, state, application_id, user_id) FROM stdin;
\.


--
-- Data for Name: feedback_feedbackentry; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.feedback_feedbackentry (id, full_name, email, comment, created_date) FROM stdin;
\.


--
-- Data for Name: history_historyentry; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.history_historyentry (id, "user", created_at, type, is_snapshot, key, diff, snapshot, "values", comment, comment_html, delete_comment_date, delete_comment_user, is_hidden, comment_versions, edit_comment_date, project_id, values_diff_cache) FROM stdin;
\.


--
-- Data for Name: issues_issue; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.issues_issue (id, tags, version, is_blocked, blocked_note, ref, created_date, modified_date, finished_date, subject, description, assigned_to_id, milestone_id, owner_id, priority_id, project_id, severity_id, status_id, type_id, external_reference, due_date, due_date_reason) FROM stdin;
\.


--
-- Data for Name: likes_like; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.likes_like (id, object_id, created_date, content_type_id, user_id) FROM stdin;
\.


--
-- Data for Name: milestones_milestone; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.milestones_milestone (id, name, slug, estimated_start, estimated_finish, created_date, modified_date, closed, disponibility, "order", owner_id, project_id) FROM stdin;
\.


--
-- Data for Name: notifications_historychangenotification; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_historychangenotification (id, key, created_datetime, updated_datetime, history_type, owner_id, project_id) FROM stdin;
\.


--
-- Data for Name: notifications_historychangenotification_history_entries; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_historychangenotification_history_entries (id, historychangenotification_id, historyentry_id) FROM stdin;
\.


--
-- Data for Name: notifications_historychangenotification_notify_users; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_historychangenotification_notify_users (id, historychangenotification_id, user_id) FROM stdin;
\.


--
-- Data for Name: notifications_notifypolicy; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_notifypolicy (id, notify_level, created_at, modified_at, project_id, user_id, live_notify_level, web_notify_level) FROM stdin;
\.


--
-- Data for Name: notifications_watched; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_watched (id, object_id, created_date, content_type_id, user_id, project_id) FROM stdin;
\.


--
-- Data for Name: notifications_webnotification; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.notifications_webnotification (id, created, read, event_type, data, user_id) FROM stdin;
\.


--
-- Data for Name: projects_epicstatus; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_epicstatus (id, name, slug, "order", is_closed, color, project_id) FROM stdin;
\.


--
-- Data for Name: projects_issueduedate; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_issueduedate (id, name, "order", by_default, color, days_to_due, project_id) FROM stdin;
\.


--
-- Data for Name: projects_issuestatus; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_issuestatus (id, name, "order", is_closed, color, project_id, slug) FROM stdin;
\.


--
-- Data for Name: projects_issuetype; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_issuetype (id, name, "order", color, project_id) FROM stdin;
\.


--
-- Data for Name: projects_membership; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_membership (id, is_admin, email, created_at, token, user_id, project_id, role_id, invited_by_id, invitation_extra_text, user_order) FROM stdin;
\.


--
-- Data for Name: projects_points; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_points (id, name, "order", value, project_id) FROM stdin;
\.


--
-- Data for Name: projects_priority; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_priority (id, name, "order", color, project_id) FROM stdin;
\.


--
-- Data for Name: projects_project; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_project (id, tags, name, slug, description, created_date, modified_date, total_milestones, total_story_points, is_backlog_activated, is_kanban_activated, is_wiki_activated, is_issues_activated, videoconferences, videoconferences_extra_data, anon_permissions, public_permissions, is_private, tags_colors, owner_id, creation_template_id, default_issue_status_id, default_issue_type_id, default_points_id, default_priority_id, default_severity_id, default_task_status_id, default_us_status_id, issues_csv_uuid, tasks_csv_uuid, userstories_csv_uuid, is_featured, is_looking_for_people, total_activity, total_activity_last_month, total_activity_last_week, total_activity_last_year, total_fans, total_fans_last_month, total_fans_last_week, total_fans_last_year, totals_updated_datetime, logo, looking_for_people_note, blocked_code, transfer_token, is_epics_activated, default_epic_status_id, epics_csv_uuid, is_contact_activated, default_swimlane_id) FROM stdin;
\.


--
-- Data for Name: projects_projectmodulesconfig; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_projectmodulesconfig (id, config, project_id) FROM stdin;
\.


--
-- Data for Name: projects_projecttemplate; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_projecttemplate (id, name, slug, description, created_date, modified_date, default_owner_role, is_backlog_activated, is_kanban_activated, is_wiki_activated, is_issues_activated, videoconferences, videoconferences_extra_data, default_options, us_statuses, points, task_statuses, issue_statuses, issue_types, priorities, severities, roles, "order", epic_statuses, is_epics_activated, is_contact_activated, epic_custom_attributes, is_looking_for_people, issue_custom_attributes, looking_for_people_note, tags, tags_colors, task_custom_attributes, us_custom_attributes, issue_duedates, task_duedates, us_duedates) FROM stdin;
1	Scrum	scrum	The agile product backlog in Scrum is a prioritized features list, containing short descriptions of all functionality desired in the product. When applying Scrum, it's not necessary to start a project with a lengthy, upfront effort to document all requirements. The Scrum product backlog is then allowed to grow and change as more is learned about the product and its customers	2014-04-22 16:48:43.596+02	2016-08-24 18:26:40.845+02	product-owner	t	f	t	t	\N		{"points": "?", "priority": "Normal", "severity": "Normal", "us_status": "New", "issue_type": "Bug", "epic_status": "New", "task_status": "New", "issue_status": "New"}	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Ready", "slug": "ready", "color": "#E44057", "order": 2, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 3, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 4, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Done", "slug": "done", "color": "#A8E440", "order": 5, "is_closed": true, "wip_limit": null, "is_archived": false}, {"name": "Archived", "slug": "archived", "color": "#A9AABC", "order": 6, "is_closed": true, "wip_limit": null, "is_archived": true}]	[{"name": "?", "order": 1, "value": null}, {"name": "0", "order": 2, "value": 0.0}, {"name": "1/2", "order": 3, "value": 0.5}, {"name": "1", "order": 4, "value": 1.0}, {"name": "2", "order": 5, "value": 2.0}, {"name": "3", "order": 6, "value": 3.0}, {"name": "5", "order": 7, "value": 5.0}, {"name": "8", "order": 8, "value": 8.0}, {"name": "10", "order": 9, "value": 10.0}, {"name": "13", "order": 10, "value": 13.0}, {"name": "20", "order": 11, "value": 20.0}, {"name": "40", "order": 12, "value": 40.0}]	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 2, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 3, "is_closed": false}, {"name": "Closed", "slug": "closed", "color": "#A8E440", "order": 4, "is_closed": true}, {"name": "Needs Info", "slug": "needs-info", "color": "#5178D3", "order": 5, "is_closed": false}]	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#40A8E4", "order": 2, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 3, "is_closed": false}, {"name": "Closed", "slug": "closed", "color": "#A8E440", "order": 4, "is_closed": true}, {"name": "Needs Info", "slug": "needs-info", "color": "#E44057", "order": 5, "is_closed": false}, {"name": "Rejected", "slug": "rejected", "color": "#A9AABC", "order": 6, "is_closed": true}, {"name": "Postponed", "slug": "posponed", "color": "#5178D3", "order": 7, "is_closed": false}]	[{"name": "Bug", "color": "#E44057", "order": 1}, {"name": "Question", "color": "#5178D3", "order": 2}, {"name": "Enhancement", "color": "#40E4CE", "order": 3}]	[{"name": "Low", "color": "#A8E440", "order": 1}, {"name": "Normal", "color": "#E4CE40", "order": 3}, {"name": "High", "color": "#E47C40", "order": 5}]	[{"name": "Wishlist", "color": "#70728F", "order": 1}, {"name": "Minor", "color": "#40A8E4", "order": 2}, {"name": "Normal", "color": "#40E47C", "order": 3}, {"name": "Important", "color": "#E4A240", "order": 4}, {"name": "Critical", "color": "#D35450", "order": 5}]	[{"name": "UX", "slug": "ux", "order": 10, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Design", "slug": "design", "order": 20, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Front", "slug": "front", "order": 30, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Back", "slug": "back", "order": 40, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Product Owner", "slug": "product-owner", "order": 50, "computable": false, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Stakeholder", "slug": "stakeholder", "order": 60, "computable": false, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "view_milestones", "view_project", "view_tasks", "view_us", "modify_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}]	1	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "Ready", "slug": "ready", "color": "#E44057", "order": 2, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 3, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 4, "is_closed": false}, {"name": "Done", "slug": "done", "color": "#A8E440", "order": 5, "is_closed": true}]	f	t	[]	f	[]		{}	{}	[]	[]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]
2	Kanban	kanban	Kanban is a method for managing knowledge work with an emphasis on just-in-time delivery while not overloading the team members. In this approach, the process, from definition of a task to its delivery to the customer, is displayed for participants to see and team members pull work from a queue.	2014-04-22 16:50:19.738+02	2016-08-24 18:26:45.365+02	product-owner	f	t	f	f	\N		{"points": "?", "priority": "Normal", "severity": "Normal", "us_status": "New", "issue_type": "Bug", "epic_status": "New", "task_status": "New", "issue_status": "New"}	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Ready", "slug": "ready", "color": "#E44057", "order": 2, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 3, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 4, "is_closed": false, "wip_limit": null, "is_archived": false}, {"name": "Done", "slug": "done", "color": "#A8E440", "order": 5, "is_closed": true, "wip_limit": null, "is_archived": false}, {"name": "Archived", "slug": "archived", "color": "#A9AABC", "order": 6, "is_closed": true, "wip_limit": null, "is_archived": true}]	[{"name": "?", "order": 1, "value": null}, {"name": "0", "order": 2, "value": 0.0}, {"name": "1/2", "order": 3, "value": 0.5}, {"name": "1", "order": 4, "value": 1.0}, {"name": "2", "order": 5, "value": 2.0}, {"name": "3", "order": 6, "value": 3.0}, {"name": "5", "order": 7, "value": 5.0}, {"name": "8", "order": 8, "value": 8.0}, {"name": "10", "order": 9, "value": 10.0}, {"name": "13", "order": 10, "value": 13.0}, {"name": "20", "order": 11, "value": 20.0}, {"name": "40", "order": 12, "value": 40.0}]	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 2, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 3, "is_closed": false}, {"name": "Closed", "slug": "closed", "color": "#A8E440", "order": 4, "is_closed": true}, {"name": "Needs Info", "slug": "needs-info", "color": "#5178D3", "order": 5, "is_closed": false}]	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#40A8E4", "order": 2, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E47C40", "order": 3, "is_closed": false}, {"name": "Closed", "slug": "closed", "color": "#A8E440", "order": 4, "is_closed": true}, {"name": "Needs Info", "slug": "needs-info", "color": "#E44057", "order": 5, "is_closed": false}, {"name": "Rejected", "slug": "rejected", "color": "#A9AABC", "order": 6, "is_closed": true}, {"name": "Postponed", "slug": "posponed", "color": "#5178D3", "order": 7, "is_closed": false}]	[{"name": "Bug", "color": "#E44057", "order": 1}, {"name": "Question", "color": "#5178D3", "order": 2}, {"name": "Enhancement", "color": "#40E4CE", "order": 3}]	[{"name": "Low", "color": "#A9AABC", "order": 1}, {"name": "Normal", "color": "#A8E440", "order": 3}, {"name": "High", "color": "#E44057", "order": 5}]	[{"name": "Wishlist", "color": "#70728F", "order": 1}, {"name": "Minor", "color": "#40E47C", "order": 2}, {"name": "Normal", "color": "#A8E440", "order": 3}, {"name": "Important", "color": "#E4CE40", "order": 4}, {"name": "Critical", "color": "#E47C40", "order": 5}]	[{"name": "UX", "slug": "ux", "order": 10, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Design", "slug": "design", "order": 20, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Front", "slug": "front", "order": 30, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Back", "slug": "back", "order": 40, "computable": true, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Product Owner", "slug": "product-owner", "order": 50, "computable": false, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "add_milestone", "modify_milestone", "delete_milestone", "view_milestones", "view_project", "add_task", "modify_task", "delete_task", "view_tasks", "add_us", "modify_us", "delete_us", "view_us", "add_wiki_page", "modify_wiki_page", "delete_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "add_epic", "modify_epic", "delete_epic", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}, {"name": "Stakeholder", "slug": "stakeholder", "order": 60, "computable": false, "permissions": ["add_issue", "modify_issue", "delete_issue", "view_issues", "view_milestones", "view_project", "view_tasks", "view_us", "modify_wiki_page", "view_wiki_pages", "add_wiki_link", "delete_wiki_link", "view_wiki_links", "view_epics", "comment_epic", "comment_us", "comment_task", "comment_issue", "comment_wiki_page"]}]	2	[{"name": "New", "slug": "new", "color": "#70728F", "order": 1, "is_closed": false}, {"name": "Ready", "slug": "ready", "color": "#E44057", "order": 2, "is_closed": false}, {"name": "In progress", "slug": "in-progress", "color": "#E47C40", "order": 3, "is_closed": false}, {"name": "Ready for test", "slug": "ready-for-test", "color": "#E4CE40", "order": 4, "is_closed": false}, {"name": "Done", "slug": "done", "color": "#A8E440", "order": 5, "is_closed": true}]	f	t	[]	f	[]		{}	{}	[]	[]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]	[{"name": "Default", "color": "#9dce0a", "order": 1, "by_default": true, "days_to_due": null}, {"name": "Due soon", "color": "#ff9900", "order": 2, "by_default": false, "days_to_due": 14}, {"name": "Past due", "color": "#E44057", "order": 3, "by_default": false, "days_to_due": 0}]
\.


--
-- Data for Name: projects_severity; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_severity (id, name, "order", color, project_id) FROM stdin;
\.


--
-- Data for Name: projects_swimlane; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_swimlane (id, name, "order", project_id) FROM stdin;
\.


--
-- Data for Name: projects_swimlaneuserstorystatus; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_swimlaneuserstorystatus (id, wip_limit, status_id, swimlane_id) FROM stdin;
\.


--
-- Data for Name: projects_taskduedate; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_taskduedate (id, name, "order", by_default, color, days_to_due, project_id) FROM stdin;
\.


--
-- Data for Name: projects_taskstatus; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_taskstatus (id, name, "order", is_closed, color, project_id, slug) FROM stdin;
\.


--
-- Data for Name: projects_userstoryduedate; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_userstoryduedate (id, name, "order", by_default, color, days_to_due, project_id) FROM stdin;
\.


--
-- Data for Name: projects_userstorystatus; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.projects_userstorystatus (id, name, "order", is_closed, color, wip_limit, project_id, slug, is_archived) FROM stdin;
\.


--
-- Data for Name: references_reference; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.references_reference (id, object_id, ref, created_at, content_type_id, project_id) FROM stdin;
\.


--
-- Data for Name: settings_userprojectsettings; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.settings_userprojectsettings (id, homepage, created_at, modified_at, project_id, user_id) FROM stdin;
\.


--
-- Data for Name: tasks_task; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.tasks_task (id, tags, version, is_blocked, blocked_note, ref, created_date, modified_date, finished_date, subject, description, is_iocaine, assigned_to_id, milestone_id, owner_id, project_id, status_id, user_story_id, taskboard_order, us_order, external_reference, due_date, due_date_reason) FROM stdin;
\.


--
-- Data for Name: telemetry_instancetelemetry; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.telemetry_instancetelemetry (id, instance_id, created_at) FROM stdin;
\.


--
-- Data for Name: timeline_timeline; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.timeline_timeline (id, object_id, namespace, event_type, project_id, data, data_content_type_id, created, content_type_id) FROM stdin;
1	5	user:5	users.user.create	\N	{"user": {"id": 5}}	6	2023-02-06 22:15:13.31677+01	6
\.


--
-- Data for Name: token_denylist_denylistedtoken; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.token_denylist_denylistedtoken (id, denylisted_at, token_id) FROM stdin;
\.


--
-- Data for Name: token_denylist_outstandingtoken; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.token_denylist_outstandingtoken (id, jti, token, created_at, expires_at, user_id) FROM stdin;
\.


--
-- Data for Name: users_authdata; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.users_authdata (id, key, value, extra, user_id) FROM stdin;
\.


--
-- Data for Name: users_role; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.users_role (id, name, slug, permissions, "order", computable, project_id) FROM stdin;
\.


--
-- Data for Name: users_user; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.users_user (id, password, last_login, is_superuser, username, email, is_active, full_name, color, bio, photo, date_joined, lang, timezone, colorize_tags, token, email_token, new_email, is_system, theme, max_private_projects, max_public_projects, max_memberships_private_projects, max_memberships_public_projects, uuid, accepted_terms, read_new_terms, verified_email, is_staff, date_cancelled) FROM stdin;
1		2023-02-06 15:01:24.293286+01	f	bitbucket-cd57aedb80fe4942bdb2132edcc18649	bitbucket-cd57aedb80fe4942bdb2132edcc18649@taiga.io	f	BitBucket	#9bd736		user/e/5/c/9/4f13837c7d2d71033ca9193b7d3b53fc2fc700a09e8d6d7c82b292afabc1/logo.png	2023-02-06 15:01:24.293335+01			f	\N	\N	\N	t		\N	\N	\N	\N	8f88779861014122b1303d34c77f2ca4	t	f	t	f	\N
2		\N	f	github-55cc9e556d054c859a04bab4010b6f3f	github-55cc9e556d054c859a04bab4010b6f3f@taiga.io	f	GitHub	#6864ca		user/0/e/c/5/5a3d6b68a728da2ac2a767726df6848f99d999ac87320c41ff9de16b6d86/logo.png	2023-02-06 15:01:40.226995+01			f	\N	\N	\N	t		\N	\N	\N	\N	d816bc8761ae4379b268eea98c7357e8	t	f	t	f	\N
3		\N	f	gitlab-c4947e55369c4bc0999f72d75f129a25	gitlab-c4947e55369c4bc0999f72d75f129a25@taiga.io	f	GitLab	#ad51c0		user/9/d/8/0/28f1f1f18dff4febe156616c82185d121332860d94fa35c200cc60bcc429/logo.png	2023-02-06 15:01:40.272966+01			f	\N	\N	\N	t		\N	\N	\N	\N	e3c29e7c4f18403e91f9c80a584d60c6	t	f	t	f	\N
4		\N	f	gogs-0a420a0f1ba740bfaa037b632cf66d07	gogs-0a420a0f1ba740bfaa037b632cf66d07@taiga.io	f	Gogs	#25514b		user/3/9/8/3/13078750b663f097a682f57d43d3fa0ed04e88404d620fba43127c1f3336/logo.png	2023-02-06 15:01:40.518841+01			f	\N	\N	\N	t		\N	\N	\N	\N	77820225c7cd49fc888b37882bf7637e	t	f	t	f	\N
5	pbkdf2_sha256$150000$q8N6u63UXtFi$udb+cdp7PkEE/KUTH3ra6DVasQdV3YoTJrxzLWi+iP8=	\N	t	taiga_admin	null@null.com	t		#e5c9d0			2023-02-06 22:15:13.31677+01			f	\N	\N	\N	f		\N	\N	\N	\N	3dd2bd3885a14008aa5757acd8715620	t	f	t	t	\N
\.


--
-- Data for Name: userstorage_storageentry; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.userstorage_storageentry (id, created_date, modified_date, key, value, owner_id) FROM stdin;
\.


--
-- Data for Name: userstories_rolepoints; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.userstories_rolepoints (id, points_id, role_id, user_story_id) FROM stdin;
\.


--
-- Data for Name: userstories_userstory; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.userstories_userstory (id, tags, version, is_blocked, blocked_note, ref, is_closed, backlog_order, created_date, modified_date, finish_date, subject, description, client_requirement, team_requirement, assigned_to_id, generated_from_issue_id, milestone_id, owner_id, project_id, status_id, sprint_order, kanban_order, external_reference, tribe_gig, due_date, due_date_reason, generated_from_task_id, from_task_ref, swimlane_id) FROM stdin;
\.


--
-- Data for Name: userstories_userstory_assigned_users; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.userstories_userstory_assigned_users (id, userstory_id, user_id) FROM stdin;
\.


--
-- Data for Name: votes_vote; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.votes_vote (id, object_id, content_type_id, user_id, created_date) FROM stdin;
\.


--
-- Data for Name: votes_votes; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.votes_votes (id, object_id, count, content_type_id) FROM stdin;
\.


--
-- Data for Name: webhooks_webhook; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.webhooks_webhook (id, url, key, project_id, name) FROM stdin;
\.


--
-- Data for Name: webhooks_webhooklog; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.webhooks_webhooklog (id, url, status, request_data, response_data, webhook_id, created, duration, request_headers, response_headers) FROM stdin;
\.


--
-- Data for Name: wiki_wikilink; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.wiki_wikilink (id, title, href, "order", project_id) FROM stdin;
\.


--
-- Data for Name: wiki_wikipage; Type: TABLE DATA; Schema: public; Owner: taiga
--

COPY public.wiki_wikipage (id, version, slug, content, created_date, modified_date, last_modifier_id, owner_id, project_id) FROM stdin;
\.


--
-- Name: attachments_attachment_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.attachments_attachment_id_seq', 1, false);


--
-- Name: auth_group_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.auth_group_id_seq', 1, false);


--
-- Name: auth_group_permissions_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.auth_group_permissions_id_seq', 1, false);


--
-- Name: auth_permission_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.auth_permission_id_seq', 272, true);


--
-- Name: contact_contactentry_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.contact_contactentry_id_seq', 1, false);


--
-- Name: custom_attributes_epiccustomattribute_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_epiccustomattribute_id_seq', 1, false);


--
-- Name: custom_attributes_epiccustomattributesvalues_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_epiccustomattributesvalues_id_seq', 1, false);


--
-- Name: custom_attributes_issuecustomattribute_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_issuecustomattribute_id_seq', 1, false);


--
-- Name: custom_attributes_issuecustomattributesvalues_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_issuecustomattributesvalues_id_seq', 1, false);


--
-- Name: custom_attributes_taskcustomattribute_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_taskcustomattribute_id_seq', 1, false);


--
-- Name: custom_attributes_taskcustomattributesvalues_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_taskcustomattributesvalues_id_seq', 1, false);


--
-- Name: custom_attributes_userstorycustomattribute_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_userstorycustomattribute_id_seq', 1, false);


--
-- Name: custom_attributes_userstorycustomattributesvalues_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.custom_attributes_userstorycustomattributesvalues_id_seq', 1, false);


--
-- Name: django_admin_log_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.django_admin_log_id_seq', 1, false);


--
-- Name: django_content_type_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.django_content_type_id_seq', 68, true);


--
-- Name: django_migrations_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.django_migrations_id_seq', 260, true);


--
-- Name: easy_thumbnails_source_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.easy_thumbnails_source_id_seq', 1, false);


--
-- Name: easy_thumbnails_thumbnail_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.easy_thumbnails_thumbnail_id_seq', 1, false);


--
-- Name: easy_thumbnails_thumbnaildimensions_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.easy_thumbnails_thumbnaildimensions_id_seq', 1, false);


--
-- Name: epics_epic_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.epics_epic_id_seq', 1, false);


--
-- Name: epics_relateduserstory_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.epics_relateduserstory_id_seq', 1, false);


--
-- Name: external_apps_applicationtoken_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.external_apps_applicationtoken_id_seq', 1, false);


--
-- Name: feedback_feedbackentry_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.feedback_feedbackentry_id_seq', 1, false);


--
-- Name: issues_issue_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.issues_issue_id_seq', 1, false);


--
-- Name: likes_like_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.likes_like_id_seq', 1, false);


--
-- Name: milestones_milestone_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.milestones_milestone_id_seq', 1, false);


--
-- Name: notifications_historychangenotification_history_entries_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_historychangenotification_history_entries_id_seq', 1, false);


--
-- Name: notifications_historychangenotification_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_historychangenotification_id_seq', 1, false);


--
-- Name: notifications_historychangenotification_notify_users_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_historychangenotification_notify_users_id_seq', 1, false);


--
-- Name: notifications_notifypolicy_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_notifypolicy_id_seq', 1, false);


--
-- Name: notifications_watched_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_watched_id_seq', 1, false);


--
-- Name: notifications_webnotification_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.notifications_webnotification_id_seq', 1, false);


--
-- Name: projects_epicstatus_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_epicstatus_id_seq', 1, false);


--
-- Name: projects_issueduedate_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_issueduedate_id_seq', 1, false);


--
-- Name: projects_issuestatus_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_issuestatus_id_seq', 1, false);


--
-- Name: projects_issuetype_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_issuetype_id_seq', 1, false);


--
-- Name: projects_membership_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_membership_id_seq', 1, false);


--
-- Name: projects_points_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_points_id_seq', 1, false);


--
-- Name: projects_priority_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_priority_id_seq', 1, false);


--
-- Name: projects_project_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_project_id_seq', 1, false);


--
-- Name: projects_projectmodulesconfig_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_projectmodulesconfig_id_seq', 1, false);


--
-- Name: projects_projecttemplate_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_projecttemplate_id_seq', 2, true);


--
-- Name: projects_severity_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_severity_id_seq', 1, false);


--
-- Name: projects_swimlane_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_swimlane_id_seq', 1, false);


--
-- Name: projects_swimlaneuserstorystatus_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_swimlaneuserstorystatus_id_seq', 1, false);


--
-- Name: projects_taskduedate_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_taskduedate_id_seq', 1, false);


--
-- Name: projects_taskstatus_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_taskstatus_id_seq', 1, false);


--
-- Name: projects_userstoryduedate_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_userstoryduedate_id_seq', 1, false);


--
-- Name: projects_userstorystatus_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.projects_userstorystatus_id_seq', 1, false);


--
-- Name: references_reference_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.references_reference_id_seq', 1, false);


--
-- Name: settings_userprojectsettings_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.settings_userprojectsettings_id_seq', 1, false);


--
-- Name: tasks_task_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.tasks_task_id_seq', 1, false);


--
-- Name: telemetry_instancetelemetry_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.telemetry_instancetelemetry_id_seq', 1, false);


--
-- Name: timeline_timeline_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.timeline_timeline_id_seq', 1, true);


--
-- Name: token_denylist_denylistedtoken_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.token_denylist_denylistedtoken_id_seq', 1, false);


--
-- Name: token_denylist_outstandingtoken_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.token_denylist_outstandingtoken_id_seq', 1, false);


--
-- Name: users_authdata_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.users_authdata_id_seq', 1, false);


--
-- Name: users_role_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.users_role_id_seq', 1, false);


--
-- Name: users_user_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.users_user_id_seq', 5, true);


--
-- Name: userstorage_storageentry_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.userstorage_storageentry_id_seq', 1, false);


--
-- Name: userstories_rolepoints_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.userstories_rolepoints_id_seq', 1, false);


--
-- Name: userstories_userstory_assigned_users_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.userstories_userstory_assigned_users_id_seq', 1, false);


--
-- Name: userstories_userstory_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.userstories_userstory_id_seq', 1, false);


--
-- Name: votes_vote_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.votes_vote_id_seq', 1, false);


--
-- Name: votes_votes_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.votes_votes_id_seq', 1, false);


--
-- Name: webhooks_webhook_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.webhooks_webhook_id_seq', 1, false);


--
-- Name: webhooks_webhooklog_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.webhooks_webhooklog_id_seq', 1, false);


--
-- Name: wiki_wikilink_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.wiki_wikilink_id_seq', 1, false);


--
-- Name: wiki_wikipage_id_seq; Type: SEQUENCE SET; Schema: public; Owner: taiga
--

SELECT pg_catalog.setval('public.wiki_wikipage_id_seq', 1, false);


--
-- Name: attachments_attachment attachments_attachment_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.attachments_attachment
    ADD CONSTRAINT attachments_attachment_pkey PRIMARY KEY (id);


--
-- Name: auth_group auth_group_name_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group
    ADD CONSTRAINT auth_group_name_key UNIQUE (name);


--
-- Name: auth_group_permissions auth_group_permissions_group_id_permission_id_0cd325b0_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group_permissions
    ADD CONSTRAINT auth_group_permissions_group_id_permission_id_0cd325b0_uniq UNIQUE (group_id, permission_id);


--
-- Name: auth_group_permissions auth_group_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group_permissions
    ADD CONSTRAINT auth_group_permissions_pkey PRIMARY KEY (id);


--
-- Name: auth_group auth_group_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group
    ADD CONSTRAINT auth_group_pkey PRIMARY KEY (id);


--
-- Name: auth_permission auth_permission_content_type_id_codename_01ab375a_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_permission
    ADD CONSTRAINT auth_permission_content_type_id_codename_01ab375a_uniq UNIQUE (content_type_id, codename);


--
-- Name: auth_permission auth_permission_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_permission
    ADD CONSTRAINT auth_permission_pkey PRIMARY KEY (id);


--
-- Name: contact_contactentry contact_contactentry_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.contact_contactentry
    ADD CONSTRAINT contact_contactentry_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_epiccustomattribute custom_attributes_epiccu_project_id_name_3850c31d_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattribute
    ADD CONSTRAINT custom_attributes_epiccu_project_id_name_3850c31d_uniq UNIQUE (project_id, name);


--
-- Name: custom_attributes_epiccustomattribute custom_attributes_epiccustomattribute_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattribute
    ADD CONSTRAINT custom_attributes_epiccustomattribute_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_epiccustomattributesvalues custom_attributes_epiccustomattributesvalues_epic_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattributesvalues
    ADD CONSTRAINT custom_attributes_epiccustomattributesvalues_epic_id_key UNIQUE (epic_id);


--
-- Name: custom_attributes_epiccustomattributesvalues custom_attributes_epiccustomattributesvalues_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattributesvalues
    ADD CONSTRAINT custom_attributes_epiccustomattributesvalues_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_issuecustomattribute custom_attributes_issuec_project_id_name_6f71f010_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattribute
    ADD CONSTRAINT custom_attributes_issuec_project_id_name_6f71f010_uniq UNIQUE (project_id, name);


--
-- Name: custom_attributes_issuecustomattribute custom_attributes_issuecustomattribute_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattribute
    ADD CONSTRAINT custom_attributes_issuecustomattribute_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_issuecustomattributesvalues custom_attributes_issuecustomattributesvalues_issue_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattributesvalues
    ADD CONSTRAINT custom_attributes_issuecustomattributesvalues_issue_id_key UNIQUE (issue_id);


--
-- Name: custom_attributes_issuecustomattributesvalues custom_attributes_issuecustomattributesvalues_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattributesvalues
    ADD CONSTRAINT custom_attributes_issuecustomattributesvalues_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_taskcustomattribute custom_attributes_taskcu_project_id_name_c1c55ac2_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattribute
    ADD CONSTRAINT custom_attributes_taskcu_project_id_name_c1c55ac2_uniq UNIQUE (project_id, name);


--
-- Name: custom_attributes_taskcustomattribute custom_attributes_taskcustomattribute_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattribute
    ADD CONSTRAINT custom_attributes_taskcustomattribute_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_taskcustomattributesvalues custom_attributes_taskcustomattributesvalues_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattributesvalues
    ADD CONSTRAINT custom_attributes_taskcustomattributesvalues_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_taskcustomattributesvalues custom_attributes_taskcustomattributesvalues_task_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattributesvalues
    ADD CONSTRAINT custom_attributes_taskcustomattributesvalues_task_id_key UNIQUE (task_id);


--
-- Name: custom_attributes_userstorycustomattribute custom_attributes_userst_project_id_name_86c6b502_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattribute
    ADD CONSTRAINT custom_attributes_userst_project_id_name_86c6b502_uniq UNIQUE (project_id, name);


--
-- Name: custom_attributes_userstorycustomattribute custom_attributes_userstorycustomattribute_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattribute
    ADD CONSTRAINT custom_attributes_userstorycustomattribute_pkey PRIMARY KEY (id);


--
-- Name: custom_attributes_userstorycustomattributesvalues custom_attributes_userstorycustomattributesva_user_story_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattributesvalues
    ADD CONSTRAINT custom_attributes_userstorycustomattributesva_user_story_id_key UNIQUE (user_story_id);


--
-- Name: custom_attributes_userstorycustomattributesvalues custom_attributes_userstorycustomattributesvalues_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattributesvalues
    ADD CONSTRAINT custom_attributes_userstorycustomattributesvalues_pkey PRIMARY KEY (id);


--
-- Name: django_admin_log django_admin_log_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_admin_log
    ADD CONSTRAINT django_admin_log_pkey PRIMARY KEY (id);


--
-- Name: django_content_type django_content_type_app_label_model_76bd3d3b_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_content_type
    ADD CONSTRAINT django_content_type_app_label_model_76bd3d3b_uniq UNIQUE (app_label, model);


--
-- Name: django_content_type django_content_type_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_content_type
    ADD CONSTRAINT django_content_type_pkey PRIMARY KEY (id);


--
-- Name: django_migrations django_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_migrations
    ADD CONSTRAINT django_migrations_pkey PRIMARY KEY (id);


--
-- Name: django_session django_session_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_session
    ADD CONSTRAINT django_session_pkey PRIMARY KEY (session_key);


--
-- Name: djmail_message djmail_message_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.djmail_message
    ADD CONSTRAINT djmail_message_pkey PRIMARY KEY (uuid);


--
-- Name: easy_thumbnails_source easy_thumbnails_source_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_source
    ADD CONSTRAINT easy_thumbnails_source_pkey PRIMARY KEY (id);


--
-- Name: easy_thumbnails_source easy_thumbnails_source_storage_hash_name_481ce32d_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_source
    ADD CONSTRAINT easy_thumbnails_source_storage_hash_name_481ce32d_uniq UNIQUE (storage_hash, name);


--
-- Name: easy_thumbnails_thumbnail easy_thumbnails_thumbnai_storage_hash_name_source_fb375270_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnail
    ADD CONSTRAINT easy_thumbnails_thumbnai_storage_hash_name_source_fb375270_uniq UNIQUE (storage_hash, name, source_id);


--
-- Name: easy_thumbnails_thumbnail easy_thumbnails_thumbnail_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnail
    ADD CONSTRAINT easy_thumbnails_thumbnail_pkey PRIMARY KEY (id);


--
-- Name: easy_thumbnails_thumbnaildimensions easy_thumbnails_thumbnaildimensions_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnaildimensions
    ADD CONSTRAINT easy_thumbnails_thumbnaildimensions_pkey PRIMARY KEY (id);


--
-- Name: easy_thumbnails_thumbnaildimensions easy_thumbnails_thumbnaildimensions_thumbnail_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnaildimensions
    ADD CONSTRAINT easy_thumbnails_thumbnaildimensions_thumbnail_id_key UNIQUE (thumbnail_id);


--
-- Name: epics_epic epics_epic_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic
    ADD CONSTRAINT epics_epic_pkey PRIMARY KEY (id);


--
-- Name: epics_relateduserstory epics_relateduserstory_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_relateduserstory
    ADD CONSTRAINT epics_relateduserstory_pkey PRIMARY KEY (id);


--
-- Name: epics_relateduserstory epics_relateduserstory_user_story_id_epic_id_ad704d40_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_relateduserstory
    ADD CONSTRAINT epics_relateduserstory_user_story_id_epic_id_ad704d40_uniq UNIQUE (user_story_id, epic_id);


--
-- Name: external_apps_applicationtoken external_apps_applicatio_application_id_user_id_b6a9e9a8_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_applicationtoken
    ADD CONSTRAINT external_apps_applicatio_application_id_user_id_b6a9e9a8_uniq UNIQUE (application_id, user_id);


--
-- Name: external_apps_application external_apps_application_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_application
    ADD CONSTRAINT external_apps_application_pkey PRIMARY KEY (id);


--
-- Name: external_apps_applicationtoken external_apps_applicationtoken_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_applicationtoken
    ADD CONSTRAINT external_apps_applicationtoken_pkey PRIMARY KEY (id);


--
-- Name: feedback_feedbackentry feedback_feedbackentry_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.feedback_feedbackentry
    ADD CONSTRAINT feedback_feedbackentry_pkey PRIMARY KEY (id);


--
-- Name: history_historyentry history_historyentry_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.history_historyentry
    ADD CONSTRAINT history_historyentry_pkey PRIMARY KEY (id);


--
-- Name: issues_issue issues_issue_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_pkey PRIMARY KEY (id);


--
-- Name: likes_like likes_like_content_type_id_object_id_user_id_e20903f0_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.likes_like
    ADD CONSTRAINT likes_like_content_type_id_object_id_user_id_e20903f0_uniq UNIQUE (content_type_id, object_id, user_id);


--
-- Name: likes_like likes_like_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.likes_like
    ADD CONSTRAINT likes_like_pkey PRIMARY KEY (id);


--
-- Name: milestones_milestone milestones_milestone_name_project_id_fe19fd36_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone
    ADD CONSTRAINT milestones_milestone_name_project_id_fe19fd36_uniq UNIQUE (name, project_id);


--
-- Name: milestones_milestone milestones_milestone_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone
    ADD CONSTRAINT milestones_milestone_pkey PRIMARY KEY (id);


--
-- Name: milestones_milestone milestones_milestone_slug_project_id_e59bac6a_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone
    ADD CONSTRAINT milestones_milestone_slug_project_id_e59bac6a_uniq UNIQUE (slug, project_id);


--
-- Name: notifications_historychangenotification_notify_users notifications_historycha_historychangenotificatio_3b0f323b_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_notify_users
    ADD CONSTRAINT notifications_historycha_historychangenotificatio_3b0f323b_uniq UNIQUE (historychangenotification_id, user_id);


--
-- Name: notifications_historychangenotification_history_entries notifications_historycha_historychangenotificatio_8fb55cdd_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_history_entries
    ADD CONSTRAINT notifications_historycha_historychangenotificatio_8fb55cdd_uniq UNIQUE (historychangenotification_id, historyentry_id);


--
-- Name: notifications_historychangenotification notifications_historycha_key_owner_id_project_id__869f948f_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification
    ADD CONSTRAINT notifications_historycha_key_owner_id_project_id__869f948f_uniq UNIQUE (key, owner_id, project_id, history_type);


--
-- Name: notifications_historychangenotification_history_entries notifications_historychangenotification_history_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_history_entries
    ADD CONSTRAINT notifications_historychangenotification_history_entries_pkey PRIMARY KEY (id);


--
-- Name: notifications_historychangenotification_notify_users notifications_historychangenotification_notify_users_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_notify_users
    ADD CONSTRAINT notifications_historychangenotification_notify_users_pkey PRIMARY KEY (id);


--
-- Name: notifications_historychangenotification notifications_historychangenotification_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification
    ADD CONSTRAINT notifications_historychangenotification_pkey PRIMARY KEY (id);


--
-- Name: notifications_notifypolicy notifications_notifypolicy_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_notifypolicy
    ADD CONSTRAINT notifications_notifypolicy_pkey PRIMARY KEY (id);


--
-- Name: notifications_notifypolicy notifications_notifypolicy_project_id_user_id_e7aa5cf2_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_notifypolicy
    ADD CONSTRAINT notifications_notifypolicy_project_id_user_id_e7aa5cf2_uniq UNIQUE (project_id, user_id);


--
-- Name: notifications_watched notifications_watched_content_type_id_object_i_e7c27769_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched
    ADD CONSTRAINT notifications_watched_content_type_id_object_i_e7c27769_uniq UNIQUE (content_type_id, object_id, user_id, project_id);


--
-- Name: notifications_watched notifications_watched_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched
    ADD CONSTRAINT notifications_watched_pkey PRIMARY KEY (id);


--
-- Name: notifications_webnotification notifications_webnotification_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_webnotification
    ADD CONSTRAINT notifications_webnotification_pkey PRIMARY KEY (id);


--
-- Name: projects_epicstatus projects_epicstatus_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_epicstatus
    ADD CONSTRAINT projects_epicstatus_pkey PRIMARY KEY (id);


--
-- Name: projects_epicstatus projects_epicstatus_project_id_name_b71c417e_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_epicstatus
    ADD CONSTRAINT projects_epicstatus_project_id_name_b71c417e_uniq UNIQUE (project_id, name);


--
-- Name: projects_epicstatus projects_epicstatus_project_id_slug_f67857e5_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_epicstatus
    ADD CONSTRAINT projects_epicstatus_project_id_slug_f67857e5_uniq UNIQUE (project_id, slug);


--
-- Name: projects_issueduedate projects_issueduedate_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issueduedate
    ADD CONSTRAINT projects_issueduedate_pkey PRIMARY KEY (id);


--
-- Name: projects_issueduedate projects_issueduedate_project_id_name_cba303bc_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issueduedate
    ADD CONSTRAINT projects_issueduedate_project_id_name_cba303bc_uniq UNIQUE (project_id, name);


--
-- Name: projects_issuestatus projects_issuestatus_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuestatus
    ADD CONSTRAINT projects_issuestatus_pkey PRIMARY KEY (id);


--
-- Name: projects_issuestatus projects_issuestatus_project_id_name_a88dd6c0_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuestatus
    ADD CONSTRAINT projects_issuestatus_project_id_name_a88dd6c0_uniq UNIQUE (project_id, name);


--
-- Name: projects_issuestatus projects_issuestatus_project_id_slug_ca3e758d_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuestatus
    ADD CONSTRAINT projects_issuestatus_project_id_slug_ca3e758d_uniq UNIQUE (project_id, slug);


--
-- Name: projects_issuetype projects_issuetype_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuetype
    ADD CONSTRAINT projects_issuetype_pkey PRIMARY KEY (id);


--
-- Name: projects_issuetype projects_issuetype_project_id_name_41b47d87_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuetype
    ADD CONSTRAINT projects_issuetype_project_id_name_41b47d87_uniq UNIQUE (project_id, name);


--
-- Name: projects_membership projects_membership_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_pkey PRIMARY KEY (id);


--
-- Name: projects_membership projects_membership_user_id_project_id_a2829f61_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_user_id_project_id_a2829f61_uniq UNIQUE (user_id, project_id);


--
-- Name: projects_points projects_points_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_points
    ADD CONSTRAINT projects_points_pkey PRIMARY KEY (id);


--
-- Name: projects_points projects_points_project_id_name_900c69f4_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_points
    ADD CONSTRAINT projects_points_project_id_name_900c69f4_uniq UNIQUE (project_id, name);


--
-- Name: projects_priority projects_priority_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_priority
    ADD CONSTRAINT projects_priority_pkey PRIMARY KEY (id);


--
-- Name: projects_priority projects_priority_project_id_name_ca316bb1_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_priority
    ADD CONSTRAINT projects_priority_project_id_name_ca316bb1_uniq UNIQUE (project_id, name);


--
-- Name: projects_project projects_project_default_epic_status_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_epic_status_id_key UNIQUE (default_epic_status_id);


--
-- Name: projects_project projects_project_default_issue_status_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_issue_status_id_key UNIQUE (default_issue_status_id);


--
-- Name: projects_project projects_project_default_issue_type_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_issue_type_id_key UNIQUE (default_issue_type_id);


--
-- Name: projects_project projects_project_default_points_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_points_id_key UNIQUE (default_points_id);


--
-- Name: projects_project projects_project_default_priority_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_priority_id_key UNIQUE (default_priority_id);


--
-- Name: projects_project projects_project_default_severity_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_severity_id_key UNIQUE (default_severity_id);


--
-- Name: projects_project projects_project_default_swimlane_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_swimlane_id_key UNIQUE (default_swimlane_id);


--
-- Name: projects_project projects_project_default_task_status_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_task_status_id_key UNIQUE (default_task_status_id);


--
-- Name: projects_project projects_project_default_us_status_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_us_status_id_key UNIQUE (default_us_status_id);


--
-- Name: projects_project projects_project_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_pkey PRIMARY KEY (id);


--
-- Name: projects_project projects_project_slug_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_slug_key UNIQUE (slug);


--
-- Name: projects_projectmodulesconfig projects_projectmodulesconfig_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projectmodulesconfig
    ADD CONSTRAINT projects_projectmodulesconfig_pkey PRIMARY KEY (id);


--
-- Name: projects_projectmodulesconfig projects_projectmodulesconfig_project_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projectmodulesconfig
    ADD CONSTRAINT projects_projectmodulesconfig_project_id_key UNIQUE (project_id);


--
-- Name: projects_projecttemplate projects_projecttemplate_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projecttemplate
    ADD CONSTRAINT projects_projecttemplate_pkey PRIMARY KEY (id);


--
-- Name: projects_projecttemplate projects_projecttemplate_slug_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projecttemplate
    ADD CONSTRAINT projects_projecttemplate_slug_key UNIQUE (slug);


--
-- Name: projects_severity projects_severity_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_severity
    ADD CONSTRAINT projects_severity_pkey PRIMARY KEY (id);


--
-- Name: projects_severity projects_severity_project_id_name_6187c456_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_severity
    ADD CONSTRAINT projects_severity_project_id_name_6187c456_uniq UNIQUE (project_id, name);


--
-- Name: projects_swimlane projects_swimlane_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlane
    ADD CONSTRAINT projects_swimlane_pkey PRIMARY KEY (id);


--
-- Name: projects_swimlane projects_swimlane_project_id_name_a949892d_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlane
    ADD CONSTRAINT projects_swimlane_project_id_name_a949892d_uniq UNIQUE (project_id, name);


--
-- Name: projects_swimlaneuserstorystatus projects_swimlaneusersto_swimlane_id_status_id_d6ff394d_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlaneuserstorystatus
    ADD CONSTRAINT projects_swimlaneusersto_swimlane_id_status_id_d6ff394d_uniq UNIQUE (swimlane_id, status_id);


--
-- Name: projects_swimlaneuserstorystatus projects_swimlaneuserstorystatus_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlaneuserstorystatus
    ADD CONSTRAINT projects_swimlaneuserstorystatus_pkey PRIMARY KEY (id);


--
-- Name: projects_taskduedate projects_taskduedate_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskduedate
    ADD CONSTRAINT projects_taskduedate_pkey PRIMARY KEY (id);


--
-- Name: projects_taskduedate projects_taskduedate_project_id_name_6270950e_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskduedate
    ADD CONSTRAINT projects_taskduedate_project_id_name_6270950e_uniq UNIQUE (project_id, name);


--
-- Name: projects_taskstatus projects_taskstatus_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskstatus
    ADD CONSTRAINT projects_taskstatus_pkey PRIMARY KEY (id);


--
-- Name: projects_taskstatus projects_taskstatus_project_id_name_4b65b78f_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskstatus
    ADD CONSTRAINT projects_taskstatus_project_id_name_4b65b78f_uniq UNIQUE (project_id, name);


--
-- Name: projects_taskstatus projects_taskstatus_project_id_slug_30401ba3_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskstatus
    ADD CONSTRAINT projects_taskstatus_project_id_slug_30401ba3_uniq UNIQUE (project_id, slug);


--
-- Name: projects_userstoryduedate projects_userstoryduedate_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstoryduedate
    ADD CONSTRAINT projects_userstoryduedate_pkey PRIMARY KEY (id);


--
-- Name: projects_userstoryduedate projects_userstoryduedate_project_id_name_177c510a_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstoryduedate
    ADD CONSTRAINT projects_userstoryduedate_project_id_name_177c510a_uniq UNIQUE (project_id, name);


--
-- Name: projects_userstorystatus projects_userstorystatus_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstorystatus
    ADD CONSTRAINT projects_userstorystatus_pkey PRIMARY KEY (id);


--
-- Name: projects_userstorystatus projects_userstorystatus_project_id_name_7c0a1351_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstorystatus
    ADD CONSTRAINT projects_userstorystatus_project_id_name_7c0a1351_uniq UNIQUE (project_id, name);


--
-- Name: projects_userstorystatus projects_userstorystatus_project_id_slug_97a888b5_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstorystatus
    ADD CONSTRAINT projects_userstorystatus_project_id_slug_97a888b5_uniq UNIQUE (project_id, slug);


--
-- Name: references_reference references_reference_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.references_reference
    ADD CONSTRAINT references_reference_pkey PRIMARY KEY (id);


--
-- Name: references_reference references_reference_project_id_ref_82d64d63_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.references_reference
    ADD CONSTRAINT references_reference_project_id_ref_82d64d63_uniq UNIQUE (project_id, ref);


--
-- Name: settings_userprojectsettings settings_userprojectsettings_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.settings_userprojectsettings
    ADD CONSTRAINT settings_userprojectsettings_pkey PRIMARY KEY (id);


--
-- Name: settings_userprojectsettings settings_userprojectsettings_project_id_user_id_330ddee9_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.settings_userprojectsettings
    ADD CONSTRAINT settings_userprojectsettings_project_id_user_id_330ddee9_uniq UNIQUE (project_id, user_id);


--
-- Name: tasks_task tasks_task_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_pkey PRIMARY KEY (id);


--
-- Name: telemetry_instancetelemetry telemetry_instancetelemetry_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.telemetry_instancetelemetry
    ADD CONSTRAINT telemetry_instancetelemetry_pkey PRIMARY KEY (id);


--
-- Name: timeline_timeline timeline_timeline_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.timeline_timeline
    ADD CONSTRAINT timeline_timeline_pkey PRIMARY KEY (id);


--
-- Name: token_denylist_denylistedtoken token_denylist_denylistedtoken_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_denylistedtoken
    ADD CONSTRAINT token_denylist_denylistedtoken_pkey PRIMARY KEY (id);


--
-- Name: token_denylist_denylistedtoken token_denylist_denylistedtoken_token_id_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_denylistedtoken
    ADD CONSTRAINT token_denylist_denylistedtoken_token_id_key UNIQUE (token_id);


--
-- Name: token_denylist_outstandingtoken token_denylist_outstandingtoken_jti_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_outstandingtoken
    ADD CONSTRAINT token_denylist_outstandingtoken_jti_key UNIQUE (jti);


--
-- Name: token_denylist_outstandingtoken token_denylist_outstandingtoken_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_outstandingtoken
    ADD CONSTRAINT token_denylist_outstandingtoken_pkey PRIMARY KEY (id);


--
-- Name: users_authdata users_authdata_key_value_7ee3acc9_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_authdata
    ADD CONSTRAINT users_authdata_key_value_7ee3acc9_uniq UNIQUE (key, value);


--
-- Name: users_authdata users_authdata_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_authdata
    ADD CONSTRAINT users_authdata_pkey PRIMARY KEY (id);


--
-- Name: users_role users_role_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_role
    ADD CONSTRAINT users_role_pkey PRIMARY KEY (id);


--
-- Name: users_role users_role_slug_project_id_db8c270c_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_role
    ADD CONSTRAINT users_role_slug_project_id_db8c270c_uniq UNIQUE (slug, project_id);


--
-- Name: users_user users_user_email_243f6e77_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_user
    ADD CONSTRAINT users_user_email_243f6e77_uniq UNIQUE (email);


--
-- Name: users_user users_user_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_user
    ADD CONSTRAINT users_user_pkey PRIMARY KEY (id);


--
-- Name: users_user users_user_username_key; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_user
    ADD CONSTRAINT users_user_username_key UNIQUE (username);


--
-- Name: users_user users_user_uuid_6fe513d7_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_user
    ADD CONSTRAINT users_user_uuid_6fe513d7_uniq UNIQUE (uuid);


--
-- Name: userstorage_storageentry userstorage_storageentry_owner_id_key_746399cb_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstorage_storageentry
    ADD CONSTRAINT userstorage_storageentry_owner_id_key_746399cb_uniq UNIQUE (owner_id, key);


--
-- Name: userstorage_storageentry userstorage_storageentry_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstorage_storageentry
    ADD CONSTRAINT userstorage_storageentry_pkey PRIMARY KEY (id);


--
-- Name: userstories_rolepoints userstories_rolepoints_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints
    ADD CONSTRAINT userstories_rolepoints_pkey PRIMARY KEY (id);


--
-- Name: userstories_rolepoints userstories_rolepoints_user_story_id_role_id_dc0ba15e_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints
    ADD CONSTRAINT userstories_rolepoints_user_story_id_role_id_dc0ba15e_uniq UNIQUE (user_story_id, role_id);


--
-- Name: userstories_userstory_assigned_users userstories_userstory_as_userstory_id_user_id_beae1231_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory_assigned_users
    ADD CONSTRAINT userstories_userstory_as_userstory_id_user_id_beae1231_uniq UNIQUE (userstory_id, user_id);


--
-- Name: userstories_userstory_assigned_users userstories_userstory_assigned_users_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory_assigned_users
    ADD CONSTRAINT userstories_userstory_assigned_users_pkey PRIMARY KEY (id);


--
-- Name: userstories_userstory userstories_userstory_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstory_pkey PRIMARY KEY (id);


--
-- Name: votes_vote votes_vote_content_type_id_object_id_user_id_97d16fa0_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_vote
    ADD CONSTRAINT votes_vote_content_type_id_object_id_user_id_97d16fa0_uniq UNIQUE (content_type_id, object_id, user_id);


--
-- Name: votes_vote votes_vote_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_vote
    ADD CONSTRAINT votes_vote_pkey PRIMARY KEY (id);


--
-- Name: votes_votes votes_votes_content_type_id_object_id_5abfc91b_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_votes
    ADD CONSTRAINT votes_votes_content_type_id_object_id_5abfc91b_uniq UNIQUE (content_type_id, object_id);


--
-- Name: votes_votes votes_votes_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_votes
    ADD CONSTRAINT votes_votes_pkey PRIMARY KEY (id);


--
-- Name: webhooks_webhook webhooks_webhook_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhook
    ADD CONSTRAINT webhooks_webhook_pkey PRIMARY KEY (id);


--
-- Name: webhooks_webhooklog webhooks_webhooklog_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhooklog
    ADD CONSTRAINT webhooks_webhooklog_pkey PRIMARY KEY (id);


--
-- Name: wiki_wikilink wiki_wikilink_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikilink
    ADD CONSTRAINT wiki_wikilink_pkey PRIMARY KEY (id);


--
-- Name: wiki_wikilink wiki_wikilink_project_id_href_a39ae7e7_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikilink
    ADD CONSTRAINT wiki_wikilink_project_id_href_a39ae7e7_uniq UNIQUE (project_id, href);


--
-- Name: wiki_wikipage wiki_wikipage_pkey; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage
    ADD CONSTRAINT wiki_wikipage_pkey PRIMARY KEY (id);


--
-- Name: wiki_wikipage wiki_wikipage_project_id_slug_cb5b63e2_uniq; Type: CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage
    ADD CONSTRAINT wiki_wikipage_project_id_slug_cb5b63e2_uniq UNIQUE (project_id, slug);


--
-- Name: attachments_attachment_content_type_id_35dd9d5d; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX attachments_attachment_content_type_id_35dd9d5d ON public.attachments_attachment USING btree (content_type_id);


--
-- Name: attachments_attachment_content_type_id_object_id_3f2e447c_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX attachments_attachment_content_type_id_object_id_3f2e447c_idx ON public.attachments_attachment USING btree (content_type_id, object_id);


--
-- Name: attachments_attachment_owner_id_720defb8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX attachments_attachment_owner_id_720defb8 ON public.attachments_attachment USING btree (owner_id);


--
-- Name: attachments_attachment_project_id_50714f52; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX attachments_attachment_project_id_50714f52 ON public.attachments_attachment USING btree (project_id);


--
-- Name: auth_group_name_a6ea08ec_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX auth_group_name_a6ea08ec_like ON public.auth_group USING btree (name varchar_pattern_ops);


--
-- Name: auth_group_permissions_group_id_b120cbf9; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX auth_group_permissions_group_id_b120cbf9 ON public.auth_group_permissions USING btree (group_id);


--
-- Name: auth_group_permissions_permission_id_84c5c92e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX auth_group_permissions_permission_id_84c5c92e ON public.auth_group_permissions USING btree (permission_id);


--
-- Name: auth_permission_content_type_id_2f476e4b; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX auth_permission_content_type_id_2f476e4b ON public.auth_permission USING btree (content_type_id);


--
-- Name: contact_contactentry_project_id_27bfec4e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX contact_contactentry_project_id_27bfec4e ON public.contact_contactentry USING btree (project_id);


--
-- Name: contact_contactentry_user_id_f1f19c5f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX contact_contactentry_user_id_f1f19c5f ON public.contact_contactentry USING btree (user_id);


--
-- Name: custom_attributes_epiccu_epic_id_d413e57a_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_epiccu_epic_id_d413e57a_idx ON public.custom_attributes_epiccustomattributesvalues USING btree (epic_id);


--
-- Name: custom_attributes_epiccustomattribute_project_id_ad2cfaa8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_epiccustomattribute_project_id_ad2cfaa8 ON public.custom_attributes_epiccustomattribute USING btree (project_id);


--
-- Name: custom_attributes_issuec_issue_id_868161f8_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_issuec_issue_id_868161f8_idx ON public.custom_attributes_issuecustomattributesvalues USING btree (issue_id);


--
-- Name: custom_attributes_issuecustomattribute_project_id_3b4acff5; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_issuecustomattribute_project_id_3b4acff5 ON public.custom_attributes_issuecustomattribute USING btree (project_id);


--
-- Name: custom_attributes_taskcu_task_id_3d1ccf5e_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_taskcu_task_id_3d1ccf5e_idx ON public.custom_attributes_taskcustomattributesvalues USING btree (task_id);


--
-- Name: custom_attributes_taskcustomattribute_project_id_f0f622a8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_taskcustomattribute_project_id_f0f622a8 ON public.custom_attributes_taskcustomattribute USING btree (project_id);


--
-- Name: custom_attributes_userst_user_story_id_99b10c43_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_userst_user_story_id_99b10c43_idx ON public.custom_attributes_userstorycustomattributesvalues USING btree (user_story_id);


--
-- Name: custom_attributes_userstorycustomattribute_project_id_2619cf6c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX custom_attributes_userstorycustomattribute_project_id_2619cf6c ON public.custom_attributes_userstorycustomattribute USING btree (project_id);


--
-- Name: django_admin_log_content_type_id_c4bce8eb; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX django_admin_log_content_type_id_c4bce8eb ON public.django_admin_log USING btree (content_type_id);


--
-- Name: django_admin_log_user_id_c564eba6; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX django_admin_log_user_id_c564eba6 ON public.django_admin_log USING btree (user_id);


--
-- Name: django_session_expire_date_a5c62663; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX django_session_expire_date_a5c62663 ON public.django_session USING btree (expire_date);


--
-- Name: django_session_session_key_c0390e0f_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX django_session_session_key_c0390e0f_like ON public.django_session USING btree (session_key varchar_pattern_ops);


--
-- Name: djmail_message_uuid_8dad4f24_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX djmail_message_uuid_8dad4f24_like ON public.djmail_message USING btree (uuid varchar_pattern_ops);


--
-- Name: easy_thumbnails_source_name_5fe0edc6; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_source_name_5fe0edc6 ON public.easy_thumbnails_source USING btree (name);


--
-- Name: easy_thumbnails_source_name_5fe0edc6_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_source_name_5fe0edc6_like ON public.easy_thumbnails_source USING btree (name varchar_pattern_ops);


--
-- Name: easy_thumbnails_source_storage_hash_946cbcc9; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_source_storage_hash_946cbcc9 ON public.easy_thumbnails_source USING btree (storage_hash);


--
-- Name: easy_thumbnails_source_storage_hash_946cbcc9_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_source_storage_hash_946cbcc9_like ON public.easy_thumbnails_source USING btree (storage_hash varchar_pattern_ops);


--
-- Name: easy_thumbnails_thumbnail_name_b5882c31; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_thumbnail_name_b5882c31 ON public.easy_thumbnails_thumbnail USING btree (name);


--
-- Name: easy_thumbnails_thumbnail_name_b5882c31_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_thumbnail_name_b5882c31_like ON public.easy_thumbnails_thumbnail USING btree (name varchar_pattern_ops);


--
-- Name: easy_thumbnails_thumbnail_source_id_5b57bc77; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_thumbnail_source_id_5b57bc77 ON public.easy_thumbnails_thumbnail USING btree (source_id);


--
-- Name: easy_thumbnails_thumbnail_storage_hash_f1435f49; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_thumbnail_storage_hash_f1435f49 ON public.easy_thumbnails_thumbnail USING btree (storage_hash);


--
-- Name: easy_thumbnails_thumbnail_storage_hash_f1435f49_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX easy_thumbnails_thumbnail_storage_hash_f1435f49_like ON public.easy_thumbnails_thumbnail USING btree (storage_hash varchar_pattern_ops);


--
-- Name: epics_epic_assigned_to_id_13e08004; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_epic_assigned_to_id_13e08004 ON public.epics_epic USING btree (assigned_to_id);


--
-- Name: epics_epic_owner_id_b09888c4; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_epic_owner_id_b09888c4 ON public.epics_epic USING btree (owner_id);


--
-- Name: epics_epic_project_id_d98aaef7; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_epic_project_id_d98aaef7 ON public.epics_epic USING btree (project_id);


--
-- Name: epics_epic_ref_aa52eb4a; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_epic_ref_aa52eb4a ON public.epics_epic USING btree (ref);


--
-- Name: epics_epic_status_id_4cf3af1a; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_epic_status_id_4cf3af1a ON public.epics_epic USING btree (status_id);


--
-- Name: epics_relateduserstory_epic_id_57605230; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_relateduserstory_epic_id_57605230 ON public.epics_relateduserstory USING btree (epic_id);


--
-- Name: epics_relateduserstory_user_story_id_329a951c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX epics_relateduserstory_user_story_id_329a951c ON public.epics_relateduserstory USING btree (user_story_id);


--
-- Name: external_apps_application_id_e9988cf8_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX external_apps_application_id_e9988cf8_like ON public.external_apps_application USING btree (id varchar_pattern_ops);


--
-- Name: external_apps_applicationtoken_application_id_0e934655; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX external_apps_applicationtoken_application_id_0e934655 ON public.external_apps_applicationtoken USING btree (application_id);


--
-- Name: external_apps_applicationtoken_application_id_0e934655_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX external_apps_applicationtoken_application_id_0e934655_like ON public.external_apps_applicationtoken USING btree (application_id varchar_pattern_ops);


--
-- Name: external_apps_applicationtoken_user_id_6e2f1e8a; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX external_apps_applicationtoken_user_id_6e2f1e8a ON public.external_apps_applicationtoken USING btree (user_id);


--
-- Name: history_historyentry_id_ff18cc9f_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX history_historyentry_id_ff18cc9f_like ON public.history_historyentry USING btree (id varchar_pattern_ops);


--
-- Name: history_historyentry_key_c088c4ae; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX history_historyentry_key_c088c4ae ON public.history_historyentry USING btree (key);


--
-- Name: history_historyentry_key_c088c4ae_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX history_historyentry_key_c088c4ae_like ON public.history_historyentry USING btree (key varchar_pattern_ops);


--
-- Name: history_historyentry_project_id_9b008f70; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX history_historyentry_project_id_9b008f70 ON public.history_historyentry USING btree (project_id);


--
-- Name: issues_issue_assigned_to_id_c6054289; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_assigned_to_id_c6054289 ON public.issues_issue USING btree (assigned_to_id);


--
-- Name: issues_issue_milestone_id_3c2695ee; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_milestone_id_3c2695ee ON public.issues_issue USING btree (milestone_id);


--
-- Name: issues_issue_owner_id_5c361b47; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_owner_id_5c361b47 ON public.issues_issue USING btree (owner_id);


--
-- Name: issues_issue_priority_id_93842a93; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_priority_id_93842a93 ON public.issues_issue USING btree (priority_id);


--
-- Name: issues_issue_project_id_4b0f3e2f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_project_id_4b0f3e2f ON public.issues_issue USING btree (project_id);


--
-- Name: issues_issue_ref_4c1e7f8f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_ref_4c1e7f8f ON public.issues_issue USING btree (ref);


--
-- Name: issues_issue_severity_id_695dade0; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_severity_id_695dade0 ON public.issues_issue USING btree (severity_id);


--
-- Name: issues_issue_status_id_64473cf1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_status_id_64473cf1 ON public.issues_issue USING btree (status_id);


--
-- Name: issues_issue_type_id_c1063362; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX issues_issue_type_id_c1063362 ON public.issues_issue USING btree (type_id);


--
-- Name: likes_like_content_type_id_8ffc2116; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX likes_like_content_type_id_8ffc2116 ON public.likes_like USING btree (content_type_id);


--
-- Name: likes_like_user_id_aae4c421; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX likes_like_user_id_aae4c421 ON public.likes_like USING btree (user_id);


--
-- Name: milestones_milestone_name_23fb0698; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_name_23fb0698 ON public.milestones_milestone USING btree (name);


--
-- Name: milestones_milestone_name_23fb0698_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_name_23fb0698_like ON public.milestones_milestone USING btree (name varchar_pattern_ops);


--
-- Name: milestones_milestone_owner_id_216ba23b; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_owner_id_216ba23b ON public.milestones_milestone USING btree (owner_id);


--
-- Name: milestones_milestone_project_id_6151cb75; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_project_id_6151cb75 ON public.milestones_milestone USING btree (project_id);


--
-- Name: milestones_milestone_slug_08e5995e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_slug_08e5995e ON public.milestones_milestone USING btree (slug);


--
-- Name: milestones_milestone_slug_08e5995e_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX milestones_milestone_slug_08e5995e_like ON public.milestones_milestone USING btree (slug varchar_pattern_ops);


--
-- Name: notifications_historycha_historyentry_id_ad550852_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historycha_historyentry_id_ad550852_like ON public.notifications_historychangenotification_history_entries USING btree (historyentry_id varchar_pattern_ops);


--
-- Name: notifications_historychang_historychangenotification__65e52ffd; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychang_historychangenotification__65e52ffd ON public.notifications_historychangenotification_history_entries USING btree (historychangenotification_id);


--
-- Name: notifications_historychang_historychangenotification__d8e98e97; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychang_historychangenotification__d8e98e97 ON public.notifications_historychangenotification_notify_users USING btree (historychangenotification_id);


--
-- Name: notifications_historychang_historyentry_id_ad550852; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychang_historyentry_id_ad550852 ON public.notifications_historychangenotification_history_entries USING btree (historyentry_id);


--
-- Name: notifications_historychang_user_id_f7bd2448; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychang_user_id_f7bd2448 ON public.notifications_historychangenotification_notify_users USING btree (user_id);


--
-- Name: notifications_historychangenotification_owner_id_6f63be8a; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychangenotification_owner_id_6f63be8a ON public.notifications_historychangenotification USING btree (owner_id);


--
-- Name: notifications_historychangenotification_project_id_52cf5e2b; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_historychangenotification_project_id_52cf5e2b ON public.notifications_historychangenotification USING btree (project_id);


--
-- Name: notifications_notifypolicy_project_id_aa5da43f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_notifypolicy_project_id_aa5da43f ON public.notifications_notifypolicy USING btree (project_id);


--
-- Name: notifications_notifypolicy_user_id_2902cbeb; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_notifypolicy_user_id_2902cbeb ON public.notifications_notifypolicy USING btree (user_id);


--
-- Name: notifications_watched_content_type_id_7b3ab729; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_watched_content_type_id_7b3ab729 ON public.notifications_watched USING btree (content_type_id);


--
-- Name: notifications_watched_project_id_c88baa46; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_watched_project_id_c88baa46 ON public.notifications_watched USING btree (project_id);


--
-- Name: notifications_watched_user_id_1bce1955; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_watched_user_id_1bce1955 ON public.notifications_watched USING btree (user_id);


--
-- Name: notifications_webnotification_created_b17f50f8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_webnotification_created_b17f50f8 ON public.notifications_webnotification USING btree (created);


--
-- Name: notifications_webnotification_user_id_f32287d5; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX notifications_webnotification_user_id_f32287d5 ON public.notifications_webnotification USING btree (user_id);


--
-- Name: projects_epicstatus_project_id_d2c43c29; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_epicstatus_project_id_d2c43c29 ON public.projects_epicstatus USING btree (project_id);


--
-- Name: projects_epicstatus_slug_63c476c8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_epicstatus_slug_63c476c8 ON public.projects_epicstatus USING btree (slug);


--
-- Name: projects_epicstatus_slug_63c476c8_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_epicstatus_slug_63c476c8_like ON public.projects_epicstatus USING btree (slug varchar_pattern_ops);


--
-- Name: projects_issueduedate_project_id_ec077eb7; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_issueduedate_project_id_ec077eb7 ON public.projects_issueduedate USING btree (project_id);


--
-- Name: projects_issuestatus_project_id_1988ebf4; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_issuestatus_project_id_1988ebf4 ON public.projects_issuestatus USING btree (project_id);


--
-- Name: projects_issuestatus_slug_2c528947; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_issuestatus_slug_2c528947 ON public.projects_issuestatus USING btree (slug);


--
-- Name: projects_issuestatus_slug_2c528947_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_issuestatus_slug_2c528947_like ON public.projects_issuestatus USING btree (slug varchar_pattern_ops);


--
-- Name: projects_issuetype_project_id_e831e4ae; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_issuetype_project_id_e831e4ae ON public.projects_issuetype USING btree (project_id);


--
-- Name: projects_membership_invited_by_id_a2c6c913; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_membership_invited_by_id_a2c6c913 ON public.projects_membership USING btree (invited_by_id);


--
-- Name: projects_membership_project_id_5f65bf3f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_membership_project_id_5f65bf3f ON public.projects_membership USING btree (project_id);


--
-- Name: projects_membership_role_id_c4bd36ef; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_membership_role_id_c4bd36ef ON public.projects_membership USING btree (role_id);


--
-- Name: projects_membership_user_id_13374535; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_membership_user_id_13374535 ON public.projects_membership USING btree (user_id);


--
-- Name: projects_points_project_id_3b8f7b42; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_points_project_id_3b8f7b42 ON public.projects_points USING btree (project_id);


--
-- Name: projects_priority_project_id_936c75b2; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_priority_project_id_936c75b2 ON public.projects_priority USING btree (project_id);


--
-- Name: projects_project_creation_template_id_b5a97819; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_creation_template_id_b5a97819 ON public.projects_project USING btree (creation_template_id);


--
-- Name: projects_project_epics_csv_uuid_cb50f2ee; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_epics_csv_uuid_cb50f2ee ON public.projects_project USING btree (epics_csv_uuid);


--
-- Name: projects_project_epics_csv_uuid_cb50f2ee_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_epics_csv_uuid_cb50f2ee_like ON public.projects_project USING btree (epics_csv_uuid varchar_pattern_ops);


--
-- Name: projects_project_issues_csv_uuid_e6a84723; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_issues_csv_uuid_e6a84723 ON public.projects_project USING btree (issues_csv_uuid);


--
-- Name: projects_project_issues_csv_uuid_e6a84723_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_issues_csv_uuid_e6a84723_like ON public.projects_project USING btree (issues_csv_uuid varchar_pattern_ops);


--
-- Name: projects_project_name_id_44f44a5f_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_name_id_44f44a5f_idx ON public.projects_project USING btree (name, id);


--
-- Name: projects_project_owner_id_b940de39; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_owner_id_b940de39 ON public.projects_project USING btree (owner_id);


--
-- Name: projects_project_slug_2d50067a_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_slug_2d50067a_like ON public.projects_project USING btree (slug varchar_pattern_ops);


--
-- Name: projects_project_tasks_csv_uuid_ecd0b1b5; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_tasks_csv_uuid_ecd0b1b5 ON public.projects_project USING btree (tasks_csv_uuid);


--
-- Name: projects_project_tasks_csv_uuid_ecd0b1b5_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_tasks_csv_uuid_ecd0b1b5_like ON public.projects_project USING btree (tasks_csv_uuid varchar_pattern_ops);


--
-- Name: projects_project_textquery_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_textquery_idx ON public.projects_project USING gin ((((setweight(to_tsvector('simple'::regconfig, (COALESCE(name, ''::character varying))::text), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, COALESCE(public.inmutable_array_to_string(tags), ''::text)), 'B'::"char")) || setweight(to_tsvector('simple'::regconfig, COALESCE(description, ''::text)), 'C'::"char"))));


--
-- Name: projects_project_total_activity_edf1a486; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_activity_edf1a486 ON public.projects_project USING btree (total_activity);


--
-- Name: projects_project_total_activity_last_month_669bff3e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_activity_last_month_669bff3e ON public.projects_project USING btree (total_activity_last_month);


--
-- Name: projects_project_total_activity_last_week_961ca1b0; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_activity_last_week_961ca1b0 ON public.projects_project USING btree (total_activity_last_week);


--
-- Name: projects_project_total_activity_last_year_12ea6dbe; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_activity_last_year_12ea6dbe ON public.projects_project USING btree (total_activity_last_year);


--
-- Name: projects_project_total_fans_436fe323; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_fans_436fe323 ON public.projects_project USING btree (total_fans);


--
-- Name: projects_project_total_fans_last_month_455afdbb; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_fans_last_month_455afdbb ON public.projects_project USING btree (total_fans_last_month);


--
-- Name: projects_project_total_fans_last_week_c65146b1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_fans_last_week_c65146b1 ON public.projects_project USING btree (total_fans_last_week);


--
-- Name: projects_project_total_fans_last_year_167b29c2; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_total_fans_last_year_167b29c2 ON public.projects_project USING btree (total_fans_last_year);


--
-- Name: projects_project_totals_updated_datetime_1bcc5bfa; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_totals_updated_datetime_1bcc5bfa ON public.projects_project USING btree (totals_updated_datetime);


--
-- Name: projects_project_userstories_csv_uuid_6e83c6c1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_userstories_csv_uuid_6e83c6c1 ON public.projects_project USING btree (userstories_csv_uuid);


--
-- Name: projects_project_userstories_csv_uuid_6e83c6c1_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_project_userstories_csv_uuid_6e83c6c1_like ON public.projects_project USING btree (userstories_csv_uuid varchar_pattern_ops);


--
-- Name: projects_projecttemplate_slug_2731738e_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_projecttemplate_slug_2731738e_like ON public.projects_projecttemplate USING btree (slug varchar_pattern_ops);


--
-- Name: projects_severity_project_id_9ab920cd; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_severity_project_id_9ab920cd ON public.projects_severity USING btree (project_id);


--
-- Name: projects_swimlane_project_id_06871cf8; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_swimlane_project_id_06871cf8 ON public.projects_swimlane USING btree (project_id);


--
-- Name: projects_swimlaneuserstorystatus_status_id_2f3fda91; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_swimlaneuserstorystatus_status_id_2f3fda91 ON public.projects_swimlaneuserstorystatus USING btree (status_id);


--
-- Name: projects_swimlaneuserstorystatus_swimlane_id_1d3f2b21; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_swimlaneuserstorystatus_swimlane_id_1d3f2b21 ON public.projects_swimlaneuserstorystatus USING btree (swimlane_id);


--
-- Name: projects_taskduedate_project_id_775d850d; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_taskduedate_project_id_775d850d ON public.projects_taskduedate USING btree (project_id);


--
-- Name: projects_taskstatus_project_id_8b32b2bb; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_taskstatus_project_id_8b32b2bb ON public.projects_taskstatus USING btree (project_id);


--
-- Name: projects_taskstatus_slug_cf358ffa; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_taskstatus_slug_cf358ffa ON public.projects_taskstatus USING btree (slug);


--
-- Name: projects_taskstatus_slug_cf358ffa_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_taskstatus_slug_cf358ffa_like ON public.projects_taskstatus USING btree (slug varchar_pattern_ops);


--
-- Name: projects_userstoryduedate_project_id_ab7b1680; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_userstoryduedate_project_id_ab7b1680 ON public.projects_userstoryduedate USING btree (project_id);


--
-- Name: projects_userstorystatus_project_id_cdf95c9c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_userstorystatus_project_id_cdf95c9c ON public.projects_userstorystatus USING btree (project_id);


--
-- Name: projects_userstorystatus_slug_d574ed51; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_userstorystatus_slug_d574ed51 ON public.projects_userstorystatus USING btree (slug);


--
-- Name: projects_userstorystatus_slug_d574ed51_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX projects_userstorystatus_slug_d574ed51_like ON public.projects_userstorystatus USING btree (slug varchar_pattern_ops);


--
-- Name: references_reference_content_type_id_c134e05e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX references_reference_content_type_id_c134e05e ON public.references_reference USING btree (content_type_id);


--
-- Name: references_reference_project_id_00275368; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX references_reference_project_id_00275368 ON public.references_reference USING btree (project_id);


--
-- Name: settings_userprojectsettings_project_id_0bc686ce; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX settings_userprojectsettings_project_id_0bc686ce ON public.settings_userprojectsettings USING btree (project_id);


--
-- Name: settings_userprojectsettings_user_id_0e7fdc25; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX settings_userprojectsettings_user_id_0e7fdc25 ON public.settings_userprojectsettings USING btree (user_id);


--
-- Name: tasks_task_assigned_to_id_e8821f61; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_assigned_to_id_e8821f61 ON public.tasks_task USING btree (assigned_to_id);


--
-- Name: tasks_task_milestone_id_64cc568f; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_milestone_id_64cc568f ON public.tasks_task USING btree (milestone_id);


--
-- Name: tasks_task_owner_id_db3dcc3e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_owner_id_db3dcc3e ON public.tasks_task USING btree (owner_id);


--
-- Name: tasks_task_project_id_a2815f0c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_project_id_a2815f0c ON public.tasks_task USING btree (project_id);


--
-- Name: tasks_task_ref_9f55bd37; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_ref_9f55bd37 ON public.tasks_task USING btree (ref);


--
-- Name: tasks_task_status_id_899d2b90; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_status_id_899d2b90 ON public.tasks_task USING btree (status_id);


--
-- Name: tasks_task_user_story_id_47ceaf1d; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX tasks_task_user_story_id_47ceaf1d ON public.tasks_task USING btree (user_story_id);


--
-- Name: timeline_ti_content_1af26f_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_ti_content_1af26f_idx ON public.timeline_timeline USING btree (content_type_id, object_id, created DESC);


--
-- Name: timeline_ti_namespa_89bca1_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_ti_namespa_89bca1_idx ON public.timeline_timeline USING btree (namespace, created DESC);


--
-- Name: timeline_timeline_content_type_id_5731a0c6; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_content_type_id_5731a0c6 ON public.timeline_timeline USING btree (content_type_id);


--
-- Name: timeline_timeline_created_4e9e3a68; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_created_4e9e3a68 ON public.timeline_timeline USING btree (created);


--
-- Name: timeline_timeline_data_content_type_id_0689742e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_data_content_type_id_0689742e ON public.timeline_timeline USING btree (data_content_type_id);


--
-- Name: timeline_timeline_event_type_cb2fcdb2; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_event_type_cb2fcdb2 ON public.timeline_timeline USING btree (event_type);


--
-- Name: timeline_timeline_event_type_cb2fcdb2_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_event_type_cb2fcdb2_like ON public.timeline_timeline USING btree (event_type varchar_pattern_ops);


--
-- Name: timeline_timeline_namespace_26f217ed; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_namespace_26f217ed ON public.timeline_timeline USING btree (namespace);


--
-- Name: timeline_timeline_namespace_26f217ed_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_namespace_26f217ed_like ON public.timeline_timeline USING btree (namespace varchar_pattern_ops);


--
-- Name: timeline_timeline_project_id_58d5eadd; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX timeline_timeline_project_id_58d5eadd ON public.timeline_timeline USING btree (project_id);


--
-- Name: token_denylist_outstandingtoken_jti_70fa66b5_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX token_denylist_outstandingtoken_jti_70fa66b5_like ON public.token_denylist_outstandingtoken USING btree (jti varchar_pattern_ops);


--
-- Name: token_denylist_outstandingtoken_user_id_c6f48986; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX token_denylist_outstandingtoken_user_id_c6f48986 ON public.token_denylist_outstandingtoken USING btree (user_id);


--
-- Name: users_authdata_key_c3b89eef; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_authdata_key_c3b89eef ON public.users_authdata USING btree (key);


--
-- Name: users_authdata_key_c3b89eef_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_authdata_key_c3b89eef_like ON public.users_authdata USING btree (key varchar_pattern_ops);


--
-- Name: users_authdata_user_id_9625853a; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_authdata_user_id_9625853a ON public.users_authdata USING btree (user_id);


--
-- Name: users_role_project_id_2837f877; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_role_project_id_2837f877 ON public.users_role USING btree (project_id);


--
-- Name: users_role_slug_ce33b471; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_role_slug_ce33b471 ON public.users_role USING btree (slug);


--
-- Name: users_role_slug_ce33b471_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_role_slug_ce33b471_like ON public.users_role USING btree (slug varchar_pattern_ops);


--
-- Name: users_user_email_243f6e77_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_user_email_243f6e77_like ON public.users_user USING btree (email varchar_pattern_ops);


--
-- Name: users_user_upper_idx; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_user_upper_idx ON public.users_user USING btree (upper('username'::text));


--
-- Name: users_user_upper_idx1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_user_upper_idx1 ON public.users_user USING btree (upper('email'::text));


--
-- Name: users_user_username_06e46fe6_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_user_username_06e46fe6_like ON public.users_user USING btree (username varchar_pattern_ops);


--
-- Name: users_user_uuid_6fe513d7_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX users_user_uuid_6fe513d7_like ON public.users_user USING btree (uuid varchar_pattern_ops);


--
-- Name: userstorage_storageentry_owner_id_c4c1ffc0; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstorage_storageentry_owner_id_c4c1ffc0 ON public.userstorage_storageentry USING btree (owner_id);


--
-- Name: userstories_rolepoints_points_id_cfcc5a79; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_rolepoints_points_id_cfcc5a79 ON public.userstories_rolepoints USING btree (points_id);


--
-- Name: userstories_rolepoints_role_id_94ac7663; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_rolepoints_role_id_94ac7663 ON public.userstories_rolepoints USING btree (role_id);


--
-- Name: userstories_rolepoints_user_story_id_ddb4c558; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_rolepoints_user_story_id_ddb4c558 ON public.userstories_rolepoints USING btree (user_story_id);


--
-- Name: userstories_userstory_assigned_to_id_5ba80653; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_assigned_to_id_5ba80653 ON public.userstories_userstory USING btree (assigned_to_id);


--
-- Name: userstories_userstory_assigned_users_user_id_6de6e8a7; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_assigned_users_user_id_6de6e8a7 ON public.userstories_userstory_assigned_users USING btree (user_id);


--
-- Name: userstories_userstory_assigned_users_userstory_id_fcb98e26; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_assigned_users_userstory_id_fcb98e26 ON public.userstories_userstory_assigned_users USING btree (userstory_id);


--
-- Name: userstories_userstory_generated_from_issue_id_afe43198; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_generated_from_issue_id_afe43198 ON public.userstories_userstory USING btree (generated_from_issue_id);


--
-- Name: userstories_userstory_generated_from_task_id_8e958d43; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_generated_from_task_id_8e958d43 ON public.userstories_userstory USING btree (generated_from_task_id);


--
-- Name: userstories_userstory_milestone_id_37f31d22; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_milestone_id_37f31d22 ON public.userstories_userstory USING btree (milestone_id);


--
-- Name: userstories_userstory_owner_id_df53c64e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_owner_id_df53c64e ON public.userstories_userstory USING btree (owner_id);


--
-- Name: userstories_userstory_project_id_03e85e9c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_project_id_03e85e9c ON public.userstories_userstory USING btree (project_id);


--
-- Name: userstories_userstory_ref_824701c0; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_ref_824701c0 ON public.userstories_userstory USING btree (ref);


--
-- Name: userstories_userstory_status_id_858671dd; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_status_id_858671dd ON public.userstories_userstory USING btree (status_id);


--
-- Name: userstories_userstory_swimlane_id_8ecab79d; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX userstories_userstory_swimlane_id_8ecab79d ON public.userstories_userstory USING btree (swimlane_id);


--
-- Name: votes_vote_content_type_id_c8375fe1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX votes_vote_content_type_id_c8375fe1 ON public.votes_vote USING btree (content_type_id);


--
-- Name: votes_vote_user_id_24a74629; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX votes_vote_user_id_24a74629 ON public.votes_vote USING btree (user_id);


--
-- Name: votes_votes_content_type_id_29583576; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX votes_votes_content_type_id_29583576 ON public.votes_votes USING btree (content_type_id);


--
-- Name: webhooks_webhook_project_id_76846b5e; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX webhooks_webhook_project_id_76846b5e ON public.webhooks_webhook USING btree (project_id);


--
-- Name: webhooks_webhooklog_webhook_id_646c2008; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX webhooks_webhooklog_webhook_id_646c2008 ON public.webhooks_webhooklog USING btree (webhook_id);


--
-- Name: wiki_wikilink_href_46ee8855; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikilink_href_46ee8855 ON public.wiki_wikilink USING btree (href);


--
-- Name: wiki_wikilink_href_46ee8855_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikilink_href_46ee8855_like ON public.wiki_wikilink USING btree (href varchar_pattern_ops);


--
-- Name: wiki_wikilink_project_id_7dc700d7; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikilink_project_id_7dc700d7 ON public.wiki_wikilink USING btree (project_id);


--
-- Name: wiki_wikipage_last_modifier_id_38be071c; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikipage_last_modifier_id_38be071c ON public.wiki_wikipage USING btree (last_modifier_id);


--
-- Name: wiki_wikipage_owner_id_f1f6c5fd; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikipage_owner_id_f1f6c5fd ON public.wiki_wikipage USING btree (owner_id);


--
-- Name: wiki_wikipage_project_id_03a1e2ca; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikipage_project_id_03a1e2ca ON public.wiki_wikipage USING btree (project_id);


--
-- Name: wiki_wikipage_slug_10d80dc1; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikipage_slug_10d80dc1 ON public.wiki_wikipage USING btree (slug);


--
-- Name: wiki_wikipage_slug_10d80dc1_like; Type: INDEX; Schema: public; Owner: taiga
--

CREATE INDEX wiki_wikipage_slug_10d80dc1_like ON public.wiki_wikipage USING btree (slug varchar_pattern_ops);


--
-- Name: custom_attributes_epiccustomattribute update_epiccustomvalues_after_remove_epiccustomattribute; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_epiccustomvalues_after_remove_epiccustomattribute AFTER DELETE ON public.custom_attributes_epiccustomattribute FOR EACH ROW EXECUTE FUNCTION public.clean_key_in_custom_attributes_values('epic_id', 'epics_epic', 'custom_attributes_epiccustomattributesvalues');


--
-- Name: custom_attributes_issuecustomattribute update_issuecustomvalues_after_remove_issuecustomattribute; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_issuecustomvalues_after_remove_issuecustomattribute AFTER DELETE ON public.custom_attributes_issuecustomattribute FOR EACH ROW EXECUTE FUNCTION public.clean_key_in_custom_attributes_values('issue_id', 'issues_issue', 'custom_attributes_issuecustomattributesvalues');


--
-- Name: epics_epic update_project_tags_colors_on_epic_insert; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_epic_insert AFTER INSERT ON public.epics_epic FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: epics_epic update_project_tags_colors_on_epic_update; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_epic_update AFTER UPDATE ON public.epics_epic FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: issues_issue update_project_tags_colors_on_issue_insert; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_issue_insert AFTER INSERT ON public.issues_issue FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: issues_issue update_project_tags_colors_on_issue_update; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_issue_update AFTER UPDATE ON public.issues_issue FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: tasks_task update_project_tags_colors_on_task_insert; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_task_insert AFTER INSERT ON public.tasks_task FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: tasks_task update_project_tags_colors_on_task_update; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_task_update AFTER UPDATE ON public.tasks_task FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: userstories_userstory update_project_tags_colors_on_userstory_insert; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_userstory_insert AFTER INSERT ON public.userstories_userstory FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: userstories_userstory update_project_tags_colors_on_userstory_update; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_project_tags_colors_on_userstory_update AFTER UPDATE ON public.userstories_userstory FOR EACH ROW EXECUTE FUNCTION public.update_project_tags_colors();


--
-- Name: custom_attributes_taskcustomattribute update_taskcustomvalues_after_remove_taskcustomattribute; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_taskcustomvalues_after_remove_taskcustomattribute AFTER DELETE ON public.custom_attributes_taskcustomattribute FOR EACH ROW EXECUTE FUNCTION public.clean_key_in_custom_attributes_values('task_id', 'tasks_task', 'custom_attributes_taskcustomattributesvalues');


--
-- Name: custom_attributes_userstorycustomattribute update_userstorycustomvalues_after_remove_userstorycustomattrib; Type: TRIGGER; Schema: public; Owner: taiga
--

CREATE TRIGGER update_userstorycustomvalues_after_remove_userstorycustomattrib AFTER DELETE ON public.custom_attributes_userstorycustomattribute FOR EACH ROW EXECUTE FUNCTION public.clean_key_in_custom_attributes_values('user_story_id', 'userstories_userstory', 'custom_attributes_userstorycustomattributesvalues');


--
-- Name: attachments_attachment attachments_attachme_content_type_id_35dd9d5d_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.attachments_attachment
    ADD CONSTRAINT attachments_attachme_content_type_id_35dd9d5d_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: attachments_attachment attachments_attachme_project_id_50714f52_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.attachments_attachment
    ADD CONSTRAINT attachments_attachme_project_id_50714f52_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: attachments_attachment attachments_attachment_owner_id_720defb8_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.attachments_attachment
    ADD CONSTRAINT attachments_attachment_owner_id_720defb8_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: auth_group_permissions auth_group_permissio_permission_id_84c5c92e_fk_auth_perm; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group_permissions
    ADD CONSTRAINT auth_group_permissio_permission_id_84c5c92e_fk_auth_perm FOREIGN KEY (permission_id) REFERENCES public.auth_permission(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: auth_group_permissions auth_group_permissions_group_id_b120cbf9_fk_auth_group_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_group_permissions
    ADD CONSTRAINT auth_group_permissions_group_id_b120cbf9_fk_auth_group_id FOREIGN KEY (group_id) REFERENCES public.auth_group(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: auth_permission auth_permission_content_type_id_2f476e4b_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.auth_permission
    ADD CONSTRAINT auth_permission_content_type_id_2f476e4b_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: contact_contactentry contact_contactentry_project_id_27bfec4e_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.contact_contactentry
    ADD CONSTRAINT contact_contactentry_project_id_27bfec4e_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: contact_contactentry contact_contactentry_user_id_f1f19c5f_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.contact_contactentry
    ADD CONSTRAINT contact_contactentry_user_id_f1f19c5f_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_epiccustomattributesvalues custom_attributes_ep_epic_id_d413e57a_fk_epics_epi; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattributesvalues
    ADD CONSTRAINT custom_attributes_ep_epic_id_d413e57a_fk_epics_epi FOREIGN KEY (epic_id) REFERENCES public.epics_epic(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_epiccustomattribute custom_attributes_ep_project_id_ad2cfaa8_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_epiccustomattribute
    ADD CONSTRAINT custom_attributes_ep_project_id_ad2cfaa8_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_issuecustomattributesvalues custom_attributes_is_issue_id_868161f8_fk_issues_is; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattributesvalues
    ADD CONSTRAINT custom_attributes_is_issue_id_868161f8_fk_issues_is FOREIGN KEY (issue_id) REFERENCES public.issues_issue(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_issuecustomattribute custom_attributes_is_project_id_3b4acff5_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_issuecustomattribute
    ADD CONSTRAINT custom_attributes_is_project_id_3b4acff5_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_taskcustomattribute custom_attributes_ta_project_id_f0f622a8_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattribute
    ADD CONSTRAINT custom_attributes_ta_project_id_f0f622a8_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_taskcustomattributesvalues custom_attributes_ta_task_id_3d1ccf5e_fk_tasks_tas; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_taskcustomattributesvalues
    ADD CONSTRAINT custom_attributes_ta_task_id_3d1ccf5e_fk_tasks_tas FOREIGN KEY (task_id) REFERENCES public.tasks_task(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_userstorycustomattribute custom_attributes_us_project_id_2619cf6c_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattribute
    ADD CONSTRAINT custom_attributes_us_project_id_2619cf6c_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: custom_attributes_userstorycustomattributesvalues custom_attributes_us_user_story_id_99b10c43_fk_userstori; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.custom_attributes_userstorycustomattributesvalues
    ADD CONSTRAINT custom_attributes_us_user_story_id_99b10c43_fk_userstori FOREIGN KEY (user_story_id) REFERENCES public.userstories_userstory(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: django_admin_log django_admin_log_content_type_id_c4bce8eb_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_admin_log
    ADD CONSTRAINT django_admin_log_content_type_id_c4bce8eb_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: django_admin_log django_admin_log_user_id_c564eba6_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.django_admin_log
    ADD CONSTRAINT django_admin_log_user_id_c564eba6_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: easy_thumbnails_thumbnail easy_thumbnails_thum_source_id_5b57bc77_fk_easy_thum; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnail
    ADD CONSTRAINT easy_thumbnails_thum_source_id_5b57bc77_fk_easy_thum FOREIGN KEY (source_id) REFERENCES public.easy_thumbnails_source(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: easy_thumbnails_thumbnaildimensions easy_thumbnails_thum_thumbnail_id_c3a0c549_fk_easy_thum; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.easy_thumbnails_thumbnaildimensions
    ADD CONSTRAINT easy_thumbnails_thum_thumbnail_id_c3a0c549_fk_easy_thum FOREIGN KEY (thumbnail_id) REFERENCES public.easy_thumbnails_thumbnail(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_epic epics_epic_assigned_to_id_13e08004_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic
    ADD CONSTRAINT epics_epic_assigned_to_id_13e08004_fk_users_user_id FOREIGN KEY (assigned_to_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_epic epics_epic_owner_id_b09888c4_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic
    ADD CONSTRAINT epics_epic_owner_id_b09888c4_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_epic epics_epic_project_id_d98aaef7_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic
    ADD CONSTRAINT epics_epic_project_id_d98aaef7_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_epic epics_epic_status_id_4cf3af1a_fk_projects_epicstatus_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_epic
    ADD CONSTRAINT epics_epic_status_id_4cf3af1a_fk_projects_epicstatus_id FOREIGN KEY (status_id) REFERENCES public.projects_epicstatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_relateduserstory epics_relatedusersto_user_story_id_329a951c_fk_userstori; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_relateduserstory
    ADD CONSTRAINT epics_relatedusersto_user_story_id_329a951c_fk_userstori FOREIGN KEY (user_story_id) REFERENCES public.userstories_userstory(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: epics_relateduserstory epics_relateduserstory_epic_id_57605230_fk_epics_epic_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.epics_relateduserstory
    ADD CONSTRAINT epics_relateduserstory_epic_id_57605230_fk_epics_epic_id FOREIGN KEY (epic_id) REFERENCES public.epics_epic(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: external_apps_applicationtoken external_apps_applic_application_id_0e934655_fk_external_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_applicationtoken
    ADD CONSTRAINT external_apps_applic_application_id_0e934655_fk_external_ FOREIGN KEY (application_id) REFERENCES public.external_apps_application(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: external_apps_applicationtoken external_apps_applic_user_id_6e2f1e8a_fk_users_use; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.external_apps_applicationtoken
    ADD CONSTRAINT external_apps_applic_user_id_6e2f1e8a_fk_users_use FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: history_historyentry history_historyentry_project_id_9b008f70_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.history_historyentry
    ADD CONSTRAINT history_historyentry_project_id_9b008f70_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_assigned_to_id_c6054289_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_assigned_to_id_c6054289_fk_users_user_id FOREIGN KEY (assigned_to_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_milestone_id_3c2695ee_fk_milestones_milestone_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_milestone_id_3c2695ee_fk_milestones_milestone_id FOREIGN KEY (milestone_id) REFERENCES public.milestones_milestone(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_owner_id_5c361b47_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_owner_id_5c361b47_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_priority_id_93842a93_fk_projects_priority_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_priority_id_93842a93_fk_projects_priority_id FOREIGN KEY (priority_id) REFERENCES public.projects_priority(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_project_id_4b0f3e2f_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_project_id_4b0f3e2f_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_severity_id_695dade0_fk_projects_severity_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_severity_id_695dade0_fk_projects_severity_id FOREIGN KEY (severity_id) REFERENCES public.projects_severity(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_status_id_64473cf1_fk_projects_issuestatus_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_status_id_64473cf1_fk_projects_issuestatus_id FOREIGN KEY (status_id) REFERENCES public.projects_issuestatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: issues_issue issues_issue_type_id_c1063362_fk_projects_issuetype_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.issues_issue
    ADD CONSTRAINT issues_issue_type_id_c1063362_fk_projects_issuetype_id FOREIGN KEY (type_id) REFERENCES public.projects_issuetype(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: likes_like likes_like_content_type_id_8ffc2116_fk_django_content_type_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.likes_like
    ADD CONSTRAINT likes_like_content_type_id_8ffc2116_fk_django_content_type_id FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: likes_like likes_like_user_id_aae4c421_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.likes_like
    ADD CONSTRAINT likes_like_user_id_aae4c421_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: milestones_milestone milestones_milestone_owner_id_216ba23b_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone
    ADD CONSTRAINT milestones_milestone_owner_id_216ba23b_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: milestones_milestone milestones_milestone_project_id_6151cb75_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.milestones_milestone
    ADD CONSTRAINT milestones_milestone_project_id_6151cb75_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification_history_entries notifications_histor_historychangenotific_65e52ffd_fk_notificat; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_history_entries
    ADD CONSTRAINT notifications_histor_historychangenotific_65e52ffd_fk_notificat FOREIGN KEY (historychangenotification_id) REFERENCES public.notifications_historychangenotification(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification_notify_users notifications_histor_historychangenotific_d8e98e97_fk_notificat; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_notify_users
    ADD CONSTRAINT notifications_histor_historychangenotific_d8e98e97_fk_notificat FOREIGN KEY (historychangenotification_id) REFERENCES public.notifications_historychangenotification(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification_history_entries notifications_histor_historyentry_id_ad550852_fk_history_h; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_history_entries
    ADD CONSTRAINT notifications_histor_historyentry_id_ad550852_fk_history_h FOREIGN KEY (historyentry_id) REFERENCES public.history_historyentry(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification notifications_histor_owner_id_6f63be8a_fk_users_use; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification
    ADD CONSTRAINT notifications_histor_owner_id_6f63be8a_fk_users_use FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification notifications_histor_project_id_52cf5e2b_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification
    ADD CONSTRAINT notifications_histor_project_id_52cf5e2b_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_historychangenotification_notify_users notifications_histor_user_id_f7bd2448_fk_users_use; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_historychangenotification_notify_users
    ADD CONSTRAINT notifications_histor_user_id_f7bd2448_fk_users_use FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_notifypolicy notifications_notify_project_id_aa5da43f_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_notifypolicy
    ADD CONSTRAINT notifications_notify_project_id_aa5da43f_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_notifypolicy notifications_notifypolicy_user_id_2902cbeb_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_notifypolicy
    ADD CONSTRAINT notifications_notifypolicy_user_id_2902cbeb_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_watched notifications_watche_content_type_id_7b3ab729_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched
    ADD CONSTRAINT notifications_watche_content_type_id_7b3ab729_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_watched notifications_watche_project_id_c88baa46_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched
    ADD CONSTRAINT notifications_watche_project_id_c88baa46_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_watched notifications_watched_user_id_1bce1955_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_watched
    ADD CONSTRAINT notifications_watched_user_id_1bce1955_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: notifications_webnotification notifications_webnotification_user_id_f32287d5_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.notifications_webnotification
    ADD CONSTRAINT notifications_webnotification_user_id_f32287d5_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_epicstatus projects_epicstatus_project_id_d2c43c29_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_epicstatus
    ADD CONSTRAINT projects_epicstatus_project_id_d2c43c29_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_issueduedate projects_issueduedat_project_id_ec077eb7_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issueduedate
    ADD CONSTRAINT projects_issueduedat_project_id_ec077eb7_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_issuestatus projects_issuestatus_project_id_1988ebf4_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuestatus
    ADD CONSTRAINT projects_issuestatus_project_id_1988ebf4_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_issuetype projects_issuetype_project_id_e831e4ae_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_issuetype
    ADD CONSTRAINT projects_issuetype_project_id_e831e4ae_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_membership projects_membership_invited_by_id_a2c6c913_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_invited_by_id_a2c6c913_fk_users_user_id FOREIGN KEY (invited_by_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_membership projects_membership_project_id_5f65bf3f_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_project_id_5f65bf3f_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_membership projects_membership_role_id_c4bd36ef_fk_users_role_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_role_id_c4bd36ef_fk_users_role_id FOREIGN KEY (role_id) REFERENCES public.users_role(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_membership projects_membership_user_id_13374535_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_membership
    ADD CONSTRAINT projects_membership_user_id_13374535_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_points projects_points_project_id_3b8f7b42_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_points
    ADD CONSTRAINT projects_points_project_id_3b8f7b42_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_priority projects_priority_project_id_936c75b2_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_priority
    ADD CONSTRAINT projects_priority_project_id_936c75b2_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_creation_template_id_b5a97819_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_creation_template_id_b5a97819_fk_projects_ FOREIGN KEY (creation_template_id) REFERENCES public.projects_projecttemplate(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_epic_status__1915e581_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_epic_status__1915e581_fk_projects_ FOREIGN KEY (default_epic_status_id) REFERENCES public.projects_epicstatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_issue_status_6aebe7fd_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_issue_status_6aebe7fd_fk_projects_ FOREIGN KEY (default_issue_status_id) REFERENCES public.projects_issuestatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_issue_type_i_89e9b202_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_issue_type_i_89e9b202_fk_projects_ FOREIGN KEY (default_issue_type_id) REFERENCES public.projects_issuetype(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_points_id_6c6701c2_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_points_id_6c6701c2_fk_projects_ FOREIGN KEY (default_points_id) REFERENCES public.projects_points(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_priority_id_498ad5e0_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_priority_id_498ad5e0_fk_projects_ FOREIGN KEY (default_priority_id) REFERENCES public.projects_priority(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_severity_id_34b7fa94_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_severity_id_34b7fa94_fk_projects_ FOREIGN KEY (default_severity_id) REFERENCES public.projects_severity(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_swimlane_id_14643d1a_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_swimlane_id_14643d1a_fk_projects_ FOREIGN KEY (default_swimlane_id) REFERENCES public.projects_swimlane(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_task_status__3be95fee_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_task_status__3be95fee_fk_projects_ FOREIGN KEY (default_task_status_id) REFERENCES public.projects_taskstatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_default_us_status_id_cc989d55_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_default_us_status_id_cc989d55_fk_projects_ FOREIGN KEY (default_us_status_id) REFERENCES public.projects_userstorystatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_project projects_project_owner_id_b940de39_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_project
    ADD CONSTRAINT projects_project_owner_id_b940de39_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_projectmodulesconfig projects_projectmodu_project_id_eff1c253_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_projectmodulesconfig
    ADD CONSTRAINT projects_projectmodu_project_id_eff1c253_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_severity projects_severity_project_id_9ab920cd_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_severity
    ADD CONSTRAINT projects_severity_project_id_9ab920cd_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_swimlane projects_swimlane_project_id_06871cf8_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlane
    ADD CONSTRAINT projects_swimlane_project_id_06871cf8_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_swimlaneuserstorystatus projects_swimlaneuse_status_id_2f3fda91_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlaneuserstorystatus
    ADD CONSTRAINT projects_swimlaneuse_status_id_2f3fda91_fk_projects_ FOREIGN KEY (status_id) REFERENCES public.projects_userstorystatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_swimlaneuserstorystatus projects_swimlaneuse_swimlane_id_1d3f2b21_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_swimlaneuserstorystatus
    ADD CONSTRAINT projects_swimlaneuse_swimlane_id_1d3f2b21_fk_projects_ FOREIGN KEY (swimlane_id) REFERENCES public.projects_swimlane(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_taskduedate projects_taskduedate_project_id_775d850d_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskduedate
    ADD CONSTRAINT projects_taskduedate_project_id_775d850d_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_taskstatus projects_taskstatus_project_id_8b32b2bb_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_taskstatus
    ADD CONSTRAINT projects_taskstatus_project_id_8b32b2bb_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_userstoryduedate projects_userstorydu_project_id_ab7b1680_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstoryduedate
    ADD CONSTRAINT projects_userstorydu_project_id_ab7b1680_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: projects_userstorystatus projects_userstoryst_project_id_cdf95c9c_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.projects_userstorystatus
    ADD CONSTRAINT projects_userstoryst_project_id_cdf95c9c_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: references_reference references_reference_content_type_id_c134e05e_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.references_reference
    ADD CONSTRAINT references_reference_content_type_id_c134e05e_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: references_reference references_reference_project_id_00275368_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.references_reference
    ADD CONSTRAINT references_reference_project_id_00275368_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: settings_userprojectsettings settings_userproject_project_id_0bc686ce_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.settings_userprojectsettings
    ADD CONSTRAINT settings_userproject_project_id_0bc686ce_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: settings_userprojectsettings settings_userprojectsettings_user_id_0e7fdc25_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.settings_userprojectsettings
    ADD CONSTRAINT settings_userprojectsettings_user_id_0e7fdc25_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_assigned_to_id_e8821f61_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_assigned_to_id_e8821f61_fk_users_user_id FOREIGN KEY (assigned_to_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_milestone_id_64cc568f_fk_milestones_milestone_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_milestone_id_64cc568f_fk_milestones_milestone_id FOREIGN KEY (milestone_id) REFERENCES public.milestones_milestone(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_owner_id_db3dcc3e_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_owner_id_db3dcc3e_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_project_id_a2815f0c_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_project_id_a2815f0c_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_status_id_899d2b90_fk_projects_taskstatus_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_status_id_899d2b90_fk_projects_taskstatus_id FOREIGN KEY (status_id) REFERENCES public.projects_taskstatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: tasks_task tasks_task_user_story_id_47ceaf1d_fk_userstories_userstory_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.tasks_task
    ADD CONSTRAINT tasks_task_user_story_id_47ceaf1d_fk_userstories_userstory_id FOREIGN KEY (user_story_id) REFERENCES public.userstories_userstory(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: timeline_timeline timeline_timeline_content_type_id_5731a0c6_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.timeline_timeline
    ADD CONSTRAINT timeline_timeline_content_type_id_5731a0c6_fk_django_co FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: timeline_timeline timeline_timeline_data_content_type_id_0689742e_fk_django_co; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.timeline_timeline
    ADD CONSTRAINT timeline_timeline_data_content_type_id_0689742e_fk_django_co FOREIGN KEY (data_content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: timeline_timeline timeline_timeline_project_id_58d5eadd_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.timeline_timeline
    ADD CONSTRAINT timeline_timeline_project_id_58d5eadd_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: token_denylist_denylistedtoken token_denylist_denyl_token_id_dca79910_fk_token_den; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_denylistedtoken
    ADD CONSTRAINT token_denylist_denyl_token_id_dca79910_fk_token_den FOREIGN KEY (token_id) REFERENCES public.token_denylist_outstandingtoken(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: token_denylist_outstandingtoken token_denylist_outst_user_id_c6f48986_fk_users_use; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.token_denylist_outstandingtoken
    ADD CONSTRAINT token_denylist_outst_user_id_c6f48986_fk_users_use FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: users_authdata users_authdata_user_id_9625853a_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_authdata
    ADD CONSTRAINT users_authdata_user_id_9625853a_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: users_role users_role_project_id_2837f877_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.users_role
    ADD CONSTRAINT users_role_project_id_2837f877_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstorage_storageentry userstorage_storageentry_owner_id_c4c1ffc0_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstorage_storageentry
    ADD CONSTRAINT userstorage_storageentry_owner_id_c4c1ffc0_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_rolepoints userstories_rolepoin_user_story_id_ddb4c558_fk_userstori; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints
    ADD CONSTRAINT userstories_rolepoin_user_story_id_ddb4c558_fk_userstori FOREIGN KEY (user_story_id) REFERENCES public.userstories_userstory(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_rolepoints userstories_rolepoints_points_id_cfcc5a79_fk_projects_points_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints
    ADD CONSTRAINT userstories_rolepoints_points_id_cfcc5a79_fk_projects_points_id FOREIGN KEY (points_id) REFERENCES public.projects_points(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_rolepoints userstories_rolepoints_role_id_94ac7663_fk_users_role_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_rolepoints
    ADD CONSTRAINT userstories_rolepoints_role_id_94ac7663_fk_users_role_id FOREIGN KEY (role_id) REFERENCES public.users_role(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_generated_from_issue_afe43198_fk_issues_is; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_generated_from_issue_afe43198_fk_issues_is FOREIGN KEY (generated_from_issue_id) REFERENCES public.issues_issue(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_generated_from_task__8e958d43_fk_tasks_tas; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_generated_from_task__8e958d43_fk_tasks_tas FOREIGN KEY (generated_from_task_id) REFERENCES public.tasks_task(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_milestone_id_37f31d22_fk_milestone; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_milestone_id_37f31d22_fk_milestone FOREIGN KEY (milestone_id) REFERENCES public.milestones_milestone(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_project_id_03e85e9c_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_project_id_03e85e9c_fk_projects_ FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_status_id_858671dd_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_status_id_858671dd_fk_projects_ FOREIGN KEY (status_id) REFERENCES public.projects_userstorystatus(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstor_swimlane_id_8ecab79d_fk_projects_; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstor_swimlane_id_8ecab79d_fk_projects_ FOREIGN KEY (swimlane_id) REFERENCES public.projects_swimlane(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory_assigned_users userstories_userstor_user_id_6de6e8a7_fk_users_use; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory_assigned_users
    ADD CONSTRAINT userstories_userstor_user_id_6de6e8a7_fk_users_use FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory_assigned_users userstories_userstor_userstory_id_fcb98e26_fk_userstori; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory_assigned_users
    ADD CONSTRAINT userstories_userstor_userstory_id_fcb98e26_fk_userstori FOREIGN KEY (userstory_id) REFERENCES public.userstories_userstory(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstory_assigned_to_id_5ba80653_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstory_assigned_to_id_5ba80653_fk_users_user_id FOREIGN KEY (assigned_to_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: userstories_userstory userstories_userstory_owner_id_df53c64e_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.userstories_userstory
    ADD CONSTRAINT userstories_userstory_owner_id_df53c64e_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: votes_vote votes_vote_content_type_id_c8375fe1_fk_django_content_type_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_vote
    ADD CONSTRAINT votes_vote_content_type_id_c8375fe1_fk_django_content_type_id FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: votes_vote votes_vote_user_id_24a74629_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_vote
    ADD CONSTRAINT votes_vote_user_id_24a74629_fk_users_user_id FOREIGN KEY (user_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: votes_votes votes_votes_content_type_id_29583576_fk_django_content_type_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.votes_votes
    ADD CONSTRAINT votes_votes_content_type_id_29583576_fk_django_content_type_id FOREIGN KEY (content_type_id) REFERENCES public.django_content_type(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: webhooks_webhook webhooks_webhook_project_id_76846b5e_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhook
    ADD CONSTRAINT webhooks_webhook_project_id_76846b5e_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: webhooks_webhooklog webhooks_webhooklog_webhook_id_646c2008_fk_webhooks_webhook_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.webhooks_webhooklog
    ADD CONSTRAINT webhooks_webhooklog_webhook_id_646c2008_fk_webhooks_webhook_id FOREIGN KEY (webhook_id) REFERENCES public.webhooks_webhook(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: wiki_wikilink wiki_wikilink_project_id_7dc700d7_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikilink
    ADD CONSTRAINT wiki_wikilink_project_id_7dc700d7_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: wiki_wikipage wiki_wikipage_last_modifier_id_38be071c_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage
    ADD CONSTRAINT wiki_wikipage_last_modifier_id_38be071c_fk_users_user_id FOREIGN KEY (last_modifier_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: wiki_wikipage wiki_wikipage_owner_id_f1f6c5fd_fk_users_user_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage
    ADD CONSTRAINT wiki_wikipage_owner_id_f1f6c5fd_fk_users_user_id FOREIGN KEY (owner_id) REFERENCES public.users_user(id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: wiki_wikipage wiki_wikipage_project_id_03a1e2ca_fk_projects_project_id; Type: FK CONSTRAINT; Schema: public; Owner: taiga
--

ALTER TABLE ONLY public.wiki_wikipage
    ADD CONSTRAINT wiki_wikipage_project_id_03a1e2ca_fk_projects_project_id FOREIGN KEY (project_id) REFERENCES public.projects_project(id) DEFERRABLE INITIALLY DEFERRED;


--
-- PostgreSQL database dump complete
--

